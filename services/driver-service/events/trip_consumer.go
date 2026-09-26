package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/repo"
	"github.com/luxipha/heyGo_backend/services/driver-service/service"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

type TripConsumer struct {
	bus *pubsub.Client
	svc service.DriverService
}

func NewTripConsumer(bus *pubsub.Client, svc service.DriverService) *TripConsumer {
	return &TripConsumer{bus: bus, svc: svc}
}

func (c *TripConsumer) Consume(ctx context.Context, topics []string) error {
	return c.bus.Consumer.SubscribeAndConsume(ctx, topics, func(ctx context.Context, msg *pubsub.Message) error {
		var envelope contracts.EventMessage
		if err := json.Unmarshal(msg.Data, &envelope); err != nil {
			return fmt.Errorf("decode event envelope: %w", err)
		}
		switch msg.Topic {
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
			return c.bus.Producer.SendMessage(ctx, contracts.DriverEventLocationUpdated, &contracts.EventMessage{EntityID: riderID, Data: envelope.Data})
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
		return c.bus.Producer.SendMessage(ctx, contracts.TripEventNoDriversFound, &contracts.EventMessage{EntityID: payload.Trip.RiderID, Data: raw})
	}
	if err != nil {
		return err
	}
	logs.L().Infow("Reserved nearby driver", "tripID", payload.Trip.Id, "driverID", candidate.Driver.Id, "distanceMeters", candidate.Distance)
	return nil
}
