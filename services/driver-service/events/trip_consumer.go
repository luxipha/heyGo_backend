package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/services/driver-service/repo"
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

func NewTripConsumer(client *kf.KafkaClient, svc service.DriverService) *TripConsumer {
	return &TripConsumer{kfClient: client, svc: svc}
}

func (c *TripConsumer) Consume(ctx context.Context, topics []string) error {
	return c.kfClient.Consumer.SubscribeAndConsume(ctx, topics, func(ctx context.Context, msg *ckafka.Message) error {
		var envelope contracts.KafkaMessage
		if err := json.Unmarshal(msg.Value, &envelope); err != nil {
			return fmt.Errorf("decode Kafka envelope: %w", err)
		}
		switch *msg.TopicPartition.Topic {
		case contracts.TripEventCreated, contracts.TripEventDriverNotInterested:
			return c.match(ctx, envelope.Data)
		case contracts.DriverCmdTripDecline:
			var response messaging.DriverTripResponseData
			if err := json.Unmarshal(envelope.Data, &response); err != nil {
				return err
			}
			_, err := c.svc.Decline(ctx, response.TripID, envelope.EntityID)
			return err // Trigger queues the acknowledgement and durable retry.
		case contracts.DriverCmdLocation:
			var location messaging.DriverLocationData
			if err := json.Unmarshal(envelope.Data, &location); err != nil {
				return err
			}
			riderID, err := c.svc.UpdateLocation(ctx, envelope.EntityID, location.Location.Latitude, location.Location.Longitude)
			if err != nil {
				return err
			}
			if riderID == "" {
				return nil
			}
			return c.kfClient.Producer.SendMessage(ctx, contracts.DriverEventLocationUpdated, &contracts.KafkaMessage{EntityID: riderID, Data: envelope.Data})
		default:
			return nil
		}
	})
}

func (c *TripConsumer) RunExpiryWorker(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_, err := c.svc.ExpireOffers(ctx)
			if err != nil {
				logs.L().Warnw("Failed to expire driver offers", "error", err)
				continue
			}
			// The outbox retry event drives rematching after commit.
		}
	}
}

func (c *TripConsumer) match(ctx context.Context, raw []byte) error {
	var payload messaging.TripEventData
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode trip event: %w", err)
	}
	candidate, err := c.svc.MatchAndReserve(ctx, payload.Trip, raw)
	if errors.Is(err, repo.ErrTripAlreadyMatched) {
		return nil // The durable offer from the first reservation is still pending.
	}
	if errors.Is(err, repo.ErrNoAvailableDriver) {
		return c.kfClient.Producer.SendMessage(ctx, contracts.TripEventNoDriversFound, &contracts.KafkaMessage{EntityID: payload.Trip.RiderID, Data: raw})
	}
	if err != nil {
		return err
	}
	logs.L().Infow("Reserved nearby driver", "tripID", payload.Trip.Id, "driverID", candidate.Driver.Id, "distanceMeters", candidate.Distance)
	return nil
}
