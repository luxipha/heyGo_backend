package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

type TripConsumer struct {
	bus *pubsub.Client
	svc repo.Service
}

func NewTripConsumer(bus *pubsub.Client, svc repo.Service) *TripConsumer {
	return &TripConsumer{bus: bus, svc: svc}
}

func (tc *TripConsumer) Consume(ctx context.Context, topics []string) error {
	return tc.bus.Consumer.SubscribeAndConsume(ctx, topics,
		func(ctx context.Context, m *pubsub.Message) error {
			var eventMsg contracts.EventMessage
			if err := json.Unmarshal(m.Data, &eventMsg); err != nil {
				return fmt.Errorf("failed to unmarshal message: %w", err)
			}

			var payload messaging.PaymentTripResponseData
			if eventMsg.Data != nil {
				if err := json.Unmarshal(eventMsg.Data, &payload); err != nil {
					return fmt.Errorf("failed to unmarshal payload: %w", err)
				}
			}

			switch m.Topic {
			case contracts.PaymentCmdCreateSession:
				if err := tc.handleTripAccepted(ctx, payload); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

func (tc *TripConsumer) handleTripAccepted(ctx context.Context, payload messaging.PaymentTripResponseData) error {
	logs.L().Infow("Processing payment for trip", "tripID", payload.TripID, "amount", payload.Amount)

	paymentSession, err := tc.svc.CreatePaymentSession(ctx,
		payload.TripID,
		payload.RiderID,
		payload.DriverID,
		int64(payload.Amount),
		payload.Currency,
	)

	if err != nil {
		return err
	}

	paymentPayload := messaging.PaymentEventSessionCreatedData{
		TripID:               payload.TripID,
		SessionID:            paymentSession.TransactionReference,
		PaymentReference:     paymentSession.PaymentReference,
		TransactionReference: paymentSession.TransactionReference,
		CheckoutURL:          paymentSession.CheckoutURL,
		Amount:               float64(payload.Amount) / 100,
		Currency:             payload.Currency,
	}

	data, err := json.Marshal(paymentPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	if err := tc.bus.Producer.SendMessageAndWait(ctx, contracts.PaymentEventSessionCreated,
		&contracts.EventMessage{
			EntityID: payload.RiderID,
			Data:     data,
		},
		30*time.Second,
	); err != nil {
		return err
	}

	logs.L().Infow("Payment session created message sent for trip", "tripID", payload.TripID)
	return nil
}
