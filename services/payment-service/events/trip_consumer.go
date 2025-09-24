package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/services/payment-service/repo"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
)

type TripConsumer struct {
	kfClient *kafka.KafkaClient
	svc      repo.Service
}

func NewTripConsumer(kfClient *kafka.KafkaClient, svc repo.Service) *TripConsumer {
	return &TripConsumer{kfClient: kfClient, svc: svc}
}

func (tc *TripConsumer) Consume(ctx context.Context, topics []string) error {
	return tc.kfClient.Consumer.SubscribeAndConsume(ctx, topics,
		func(ctx context.Context, m *ckafka.Message) error {
			var kafkaMsg contracts.KafkaMessage
			if err := json.Unmarshal(m.Value, &kafkaMsg); err != nil {
				return fmt.Errorf("failed to unmarshal message: %w", err)
			}

			var payload messaging.PaymentTripResponseData
			if kafkaMsg.Data != nil {
				if err := json.Unmarshal(kafkaMsg.Data, &payload); err != nil {
					return fmt.Errorf("failed to unmarshal payload: %w", err)
				}
			}

			switch *m.TopicPartition.Topic {
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
		TripID:    payload.TripID,
		SessionID: paymentSession.StripeSessionID,
		Amount:    float64(payload.Amount) / 100, // converting paise to rupees
		Currency:  payload.Currency,
	}

	data, err := json.Marshal(paymentPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	if err := tc.kfClient.Producer.SendMessageAndWait(ctx, contracts.PaymentEventSessionCreated,
		&contracts.KafkaMessage{
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
