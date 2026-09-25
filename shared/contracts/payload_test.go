package contracts

import (
	"encoding/json"
	"testing"
)

func TestEveryTopicHasAValidatedPayloadContract(t *testing.T) {
	payloads := map[string]map[string]any{
		TripEventCreated:               {"trip": map[string]any{"id": "trip-1"}},
		TripEventDriverAssigned:        {"id": "trip-1", "riderID": "rider-1", "driver": map[string]any{"id": "driver-1"}},
		TripEventArrived:               {"trip": map[string]any{"id": "trip-1"}},
		TripEventSettlementUpdated:     {"tripId": "trip-1", "status": "pending", "expectedAmountKobo": 1000},
		TripEventNoShowClaimUpdated:    {"tripId": "trip-1", "claimId": "claim-1", "status": "pending", "feeKobo": 100000},
		TripEventStarted:               {"trip": map[string]any{"id": "trip-1"}},
		TripEventCompleted:             {"trip": map[string]any{"id": "trip-1"}},
		TripEventCancelled:             {"trip": map[string]any{"id": "trip-1"}},
		TripEventNoDriversFound:        {"trip": map[string]any{"id": "trip-1"}},
		TripEventDriverNotInterested:   {"trip": map[string]any{"id": "trip-1"}},
		TripEventExpired:               {"tripId": "trip-1", "status": "expired"},
		TripEventReassigned:            {"tripId": "trip-1", "status": "reassigned"},
		DriverCmdTripRequest:           {"trip": map[string]any{"id": "trip-1"}, "offer": map[string]any{"expiresAt": "2026-09-24T10:00:20Z", "attempt": 1}},
		DriverCmdTripAccept:            {"tripID": "trip-1", "driver": map[string]any{"id": "driver-1"}},
		DriverCmdTripDecline:           {"tripID": "trip-1", "driver": map[string]any{"id": "driver-1"}},
		DriverCmdLocation:              {"location": map[string]any{"latitude": 6.45, "longitude": 3.39}},
		DriverCmdRegister:              {"id": "driver-1"},
		DriverEventLocationUpdated:     {"location": map[string]any{"latitude": 6.45, "longitude": 3.39}},
		DriverEventCommandAcknowledged: {"tripId": "trip-1", "command": DriverCmdTripAccept, "status": "accepted"},
		PaymentEventSessionCreated:     {"tripID": "trip-1", "sessionID": "session-1", "paymentReference": "pay-1", "checkoutURL": "https://pay.test", "amount": 100, "currency": "NGN"},
		PaymentEventSuccess:            {"eventID": "event-1", "tripID": "trip-1", "paymentReference": "pay-1", "status": "success"},
		PaymentEventFailed:             {"eventID": "event-1", "tripID": "trip-1", "paymentReference": "pay-1", "status": "failed"},
		PaymentEventCancelled:          {"eventID": "event-1", "tripID": "trip-1", "paymentReference": "pay-1", "status": "cancelled"},
		PaymentCmdCreateSession:        {"tripID": "trip-1", "riderID": "rider-1", "driverID": "driver-1", "amount": 100, "currency": "NGN"},
		TripCmdComplete:                {"tripID": "trip-1", "actorID": "driver-1"},
		TripCmdArrive:                  {"tripID": "trip-1", "actorID": "driver-1"},
		TripCmdStart:                   {"tripID": "trip-1", "actorID": "driver-1"},
		TripCmdCancel:                  {"tripID": "trip-1", "actorID": "rider-1", "reason": "changed plans"},
		TripCmdRate:                    {"tripID": "trip-1", "actorID": "rider-1", "rating": 5},
	}
	for _, topic := range AllTopics() {
		t.Run(topic, func(t *testing.T) {
			payload, ok := payloads[topic]
			if !ok {
				t.Fatalf("topic %s has no contract fixture", topic)
			}
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateTopicPayload(topic, data); err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
		})
	}
}

func TestPayloadContractRejectsMissingRequiredField(t *testing.T) {
	if err := ValidateTopicPayload(PaymentEventSuccess, []byte(`{"tripID":"trip-1"}`)); err == nil {
		t.Fatal("malformed payment event accepted")
	}
}
