package events

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/services/driver-service/service"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	kf "github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
)

type TripConsumer struct {
	kfClient *kf.KafkaClient
	svc      service.DriverService
}

// NewTripEventConsumer creates a new TripEventConsumer with the given Kafka consumer.
func NewTripConsumer(kfClient *kf.KafkaClient, svc service.DriverService) *TripConsumer {
	return &TripConsumer{kfClient: kfClient, svc: svc}
}

// Consume starts consuming to the specified topics and processes messages.
func (tec *TripConsumer) Consume(ctx context.Context, topics []string) error {
	return tec.kfClient.Consumer.SubscribeAndConsume(ctx, topics,
		func(ctx context.Context, msg *kafka.Message) error {

			var kafkaMsg contracts.KafkaMessage
			if err := json.Unmarshal(msg.Value, &kafkaMsg); err != nil {
				return fmt.Errorf("failed to unmarshal message: %w", err)
			}

			logs.L().Infow("Received message on topic", "topic", *msg.TopicPartition.Topic)

			var payload messaging.TripEventData
			if err := json.Unmarshal(kafkaMsg.Data, &payload); err != nil {
				return fmt.Errorf("failed to unmarshal payload: %w", err)
			}

			// Handle different event types
			switch *msg.TopicPartition.Topic {
			case contracts.TripEventCreated, contracts.TripEventDriverNotInterested:
				return tec.handleFindAndNotifyDrivers(ctx, &payload)
			}

			logs.L().Warnw("Unknown trip event", "topic", *msg.TopicPartition.Topic)
			return nil
		},
	)
}

func (tec *TripConsumer) handleFindAndNotifyDrivers(ctx context.Context, payload *messaging.TripEventData) error {
	drivers := tec.svc.FindAvailableDrivers(ctx, payload.Trip.SelectedFare.PackageSlug)
	if len(drivers) == 0 {
		logs.L().Infow("No drivers available for trip", "tripID", payload.Trip.Id)

		// Notify trip service about unavailability of drivers
		if err := tec.kfClient.Producer.SendMessage(ctx, contracts.TripEventNoDriversFound, &contracts.KafkaMessage{
			EntityID: payload.Trip.RiderID,
		}); err != nil {
			return err
		}
		return nil
	}

	randIdx := rand.IntN(len(drivers))
	selectedDriverID := drivers[randIdx]

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	// Notify trip service about the selected driver
	if err := tec.kfClient.Producer.SendMessage(ctx, contracts.DriverCmdTripRequest, &contracts.KafkaMessage{
		EntityID: selectedDriverID,
		Data:     data,
	}); err != nil {
		return err
	}

	logs.L().Infow("Found a suitable driver for trip", "driverID", selectedDriverID, "tripID", payload.Trip.Id)
	return nil
}
