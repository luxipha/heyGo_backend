package events

import (
	"context"
	"encoding/json"
	"fmt"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/services/trip-service/service"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	pbd "github.com/cprakhar/uber-clone/shared/proto/driver"
	pb "github.com/cprakhar/uber-clone/shared/proto/trip"
)

type DriverConsumer struct {
	kfClient *kafka.KafkaClient
	svc      service.TripService
}

// NewDriverConsumer creates a new DriverConsumer with the given Kafka consumer.
func NewDriverConsumer(kfClient *kafka.KafkaClient, svc service.TripService) *DriverConsumer {
	return &DriverConsumer{kfClient: kfClient, svc: svc}
}

// Consume starts consuming messages from the specified topics and processes them.
func (dc *DriverConsumer) Consume(ctx context.Context, topics []string) error {
	return dc.kfClient.Consumer.SubscribeAndConsume(ctx, topics,
		func(ctx context.Context, msg *ckafka.Message) error {
			var kafkaMsg contracts.KafkaMessage
			if err := json.Unmarshal(msg.Value, &kafkaMsg); err != nil {
				return fmt.Errorf("failed to unmarshal message: %w", err)
			}

			var payload messaging.DriverTripResponseData
			if kafkaMsg.Data != nil {
				if err := json.Unmarshal(kafkaMsg.Data, &payload); err != nil {
					return fmt.Errorf("failed to unmarshal payload: %w", err)
				}
			}

			// Handle different driver commands
			switch *msg.TopicPartition.Topic {
			case contracts.DriverCmdTripAccept:
				if err := dc.handleTripAccept(ctx, payload.TripID, payload.Driver); err != nil {
					return err
				}
			case contracts.DriverCmdTripDecline:
				if err := dc.handleTripDecline(ctx, payload.TripID); err != nil {
					return err
				}
			default:
				logs.L().Warnw("Unknown topic", "topic", *msg.TopicPartition.Topic)
				return nil
			}

			logs.L().Infow("Processed message", "topic", *msg.TopicPartition.Topic)
			return nil
		},
	)
}

func (dc *DriverConsumer) handleTripDecline(ctx context.Context, tripID string) error {
	trip, err := dc.svc.GetTripByID(ctx, tripID)
	if err != nil {
		return err
	}

	tripEventData := &messaging.TripEventData{
		Trip: trip.ToProto(),
	}

	data, err := json.Marshal(tripEventData)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	// Notify driver service to find another driver
	if err := dc.kfClient.Producer.SendMessage(ctx, contracts.TripEventDriverNotInterested, &contracts.KafkaMessage{
		EntityID: trip.RiderID,
		Data:     data,
	}); err != nil {
		return err
	}

	return nil
}

// handleTripAccept processes a trip acceptance from a driver.
func (dc *DriverConsumer) handleTripAccept(ctx context.Context, tripID string, driver *pbd.Driver) error {
	updatedTrip, err := dc.svc.AcceptRide(ctx, tripID, &pb.TripDriver{
		Id:         driver.Id,
		Name:       driver.Name,
		ProfilePic: driver.ProfilePic,
		CarPlate:   driver.CarPlate,
	})
	if err != nil {
		return err
	}

	data, err := json.Marshal(updatedTrip)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	// Notify rider about driver assignment
	if err := dc.kfClient.Producer.SendMessage(ctx, contracts.TripEventDriverAssigned, &contracts.KafkaMessage{
		EntityID: updatedTrip.RiderID,
		Data:     data,
	}); err != nil {
		return err
	}

	paymentTripResponseData := &messaging.PaymentTripResponseData{
		TripID:   tripID,
		RiderID:  updatedTrip.RiderID,
		DriverID: driver.Id,
		Amount:   updatedTrip.RideFare.TotalFareInPaise,
		Currency: "INR",
	}

	data, err = json.Marshal(paymentTripResponseData)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	// Notify payment service to create a payment session
	if err := dc.kfClient.Producer.SendMessage(ctx, contracts.PaymentCmdCreateSession, &contracts.KafkaMessage{
		EntityID: updatedTrip.RiderID,
		Data:     data,
	}); err != nil {
		return err
	}

	logs.L().Infow("Trip accepted by driver", "tripID", tripID, "driverID", driver.Id)
	return nil
}
