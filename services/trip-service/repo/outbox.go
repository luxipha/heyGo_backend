package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	"github.com/jackc/pgx/v5"
)

func enqueueTripEvent(ctx context.Context, tx pgx.Tx, trip *types.TripModel, topic string) error {
	data, err := json.Marshal(&messaging.TripEventData{Trip: trip.ToProto()})
	if topic == contracts.TripEventDriverAssigned {
		data, err = json.Marshal(trip.ToProto())
	}
	if err == nil && trip.RideFare != nil && trip.RideFare.NoShowDebtKobo > 0 {
		data, err = withNoShowDebt(data, topic == contracts.TripEventDriverAssigned, trip.RideFare.NoShowDebtKobo, int64(math.Round(trip.RideFare.TotalFareInPaise)))
	}
	if err != nil {
		return fmt.Errorf("encode trip outbox event: %w", err)
	}
	recipients := []string{trip.RiderID}
	if trip.Driver != nil && trip.Driver.Id != "" && trip.Driver.Id != trip.RiderID && topic != contracts.TripEventCreated {
		recipients = append(recipients, trip.Driver.Id)
	}
	for _, recipient := range recipients {
		if _, err := tx.Exec(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id)
			VALUES($1::UUID,$2,$3::UUID,$4::JSONB,$5) ON CONFLICT DO NOTHING`,
			trip.ID, topic, recipient, data, correlation.FromContext(ctx)); err != nil {
			return fmt.Errorf("enqueue trip outbox event: %w", err)
		}
	}
	return nil
}

func withNoShowDebt(data []byte, bareTrip bool, debtKobo, fareKobo int64) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	tripKey := "trip"
	if bareTrip {
		tripKey = ""
	}
	tripData := data
	if tripKey != "" {
		if raw, ok := root[tripKey]; ok {
			tripData = raw
		} else {
			return nil, fmt.Errorf("trip event has no trip object")
		}
	}
	var trip map[string]json.RawMessage
	if err := json.Unmarshal(tripData, &trip); err != nil {
		return nil, err
	}
	var fare map[string]json.RawMessage
	if raw, ok := trip["selectedFare"]; ok {
		if err := json.Unmarshal(raw, &fare); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("trip event has no selected fare")
	}
	debt, _ := json.Marshal(debtKobo)
	total, _ := json.Marshal(fareKobo + debtKobo)
	fare["noShowDebtKobo"] = debt
	fare["amountDueKobo"] = total
	updatedFare, err := json.Marshal(fare)
	if err != nil {
		return nil, err
	}
	trip["selectedFare"] = updatedFare
	trip["noShowDebtKobo"] = debt
	trip["amountDueKobo"] = total
	updatedTrip, err := json.Marshal(trip)
	if err != nil {
		return nil, err
	}
	if tripKey != "" {
		root[tripKey] = updatedTrip
		return json.Marshal(root)
	}
	return updatedTrip, nil
}

func enqueueAcceptedAck(ctx context.Context, tx pgx.Tx, trip *types.TripModel) error {
	ack, err := json.Marshal(map[string]any{"command": contracts.DriverCmdTripAccept, "tripId": trip.ID, "status": "accepted"})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id)
		VALUES($1::UUID,$2,$3::UUID,$4::JSONB,$5) ON CONFLICT DO NOTHING`,
		trip.ID, contracts.DriverEventCommandAcknowledged, trip.Driver.Id, ack, correlation.FromContext(ctx))
	return err
}

func enqueueSettlementEvent(ctx context.Context, tx pgx.Tx, tripID, riderID, driverID string, amountKobo int64, status string, attempt int) error {
	payload, err := json.Marshal(map[string]any{"tripId": tripID, "status": status, "expectedAmountKobo": amountKobo})
	if err != nil {
		return fmt.Errorf("encode settlement outbox event: %w", err)
	}
	for _, recipientID := range []string{riderID, driverID} {
		if _, err := tx.Exec(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id,attempt)
			VALUES($1::UUID,$2,$3::UUID,$4::JSONB,$5,$6) ON CONFLICT DO NOTHING`,
			tripID, contracts.TripEventSettlementUpdated, recipientID, payload, correlation.FromContext(ctx), attempt); err != nil {
			return fmt.Errorf("enqueue settlement outbox event: %w", err)
		}
	}
	return nil
}
