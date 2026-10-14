package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"github.com/luxipha/heyGo_backend/services/trip-service/service"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	pbd "github.com/luxipha/heyGo_backend/shared/proto/driver"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
)

type DriverConsumer struct {
	bus  *pubsub.Client
	svc  service.TripService
	pool *pgxpool.Pool
}

// NewDriverConsumer creates a driver command consumer.
func NewDriverConsumer(bus *pubsub.Client, svc service.TripService, pools ...*pgxpool.Pool) *DriverConsumer {
	consumer := &DriverConsumer{bus: bus, svc: svc}
	if len(pools) > 0 {
		consumer.pool = pools[0]
	}
	return consumer
}

// Consume starts consuming messages from the specified topics and processes them.
func (dc *DriverConsumer) Consume(ctx context.Context, topics []string) error {
	return dc.bus.Consumer.SubscribeAndConsume(ctx, topics, dc.Handle)
}

func (dc *DriverConsumer) Handle(ctx context.Context, msg *pubsub.Message) error {
	var eventMsg contracts.EventMessage
	if err := json.Unmarshal(msg.Data, &eventMsg); err != nil {
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	switch msg.Topic {
	case contracts.DriverCmdTripAccept:
		var payload messaging.DriverTripResponseData
		if err := json.Unmarshal(eventMsg.Data, &payload); err != nil {
			return fmt.Errorf("failed to unmarshal driver acceptance: %w", err)
		}
		if err := dc.handleTripAccept(ctx, payload.TripID, payload.Driver); err != nil {
			return err
		}
	case contracts.TripCmdArrive, contracts.TripCmdStart, contracts.TripCmdComplete, contracts.TripCmdCancel, contracts.TripCmdRate:
		var payload messaging.TripLifecycleCommand
		if err := json.Unmarshal(eventMsg.Data, &payload); err != nil {
			return fmt.Errorf("decode lifecycle command: %w", err)
		}
		if payload.ActorID != eventMsg.EntityID {
			return fmt.Errorf("lifecycle actor mismatch")
		}
		if payload.CommandID != "" {
			return dc.handleRiderCommand(ctx, msg.Topic, payload)
		}
		if err := dc.handleLifecycle(ctx, msg.Topic, payload); err != nil {
			return err
		}
	default:
		logs.L().Warnw("Unknown topic", "topic", msg.Topic)
		return nil
	}

	logs.L().Infow("Processed message", "topic", msg.Topic)
	return nil
}

func (dc *DriverConsumer) handleLifecycle(ctx context.Context, command string, p messaging.TripLifecycleCommand) error {
	switch command {
	case contracts.TripCmdArrive:
		_, _, err := dc.svc.ArriveAtPickup(ctx, p.TripID, p.ActorID)
		return err
	case contracts.TripCmdStart:
		_, _, err := dc.svc.StartTrip(ctx, p.TripID, p.ActorID)
		return err
	case contracts.TripCmdComplete:
		_, _, err := dc.svc.CompleteTrip(ctx, p.TripID, p.ActorID)
		if err != nil {
			return err
		}
		return nil
	case contracts.TripCmdCancel:
		_, _, err := dc.svc.CancelTrip(ctx, p.TripID, p.ActorID, p.Reason)
		if err != nil {
			return err
		}
		return nil
	case contracts.TripCmdRate:
		_, _, err := dc.svc.RateTrip(ctx, p.TripID, p.ActorID, p.Rating, p.FeedbackTags, p.Comment)
		if err != nil {
			return err
		}
		return nil
	}
	return nil
}

// handleTripAccept processes a trip acceptance from a driver.
func (dc *DriverConsumer) handleTripAccept(ctx context.Context, tripID string, driver *pbd.Driver) error {
	_, err := dc.svc.AcceptRide(ctx, tripID, &pb.TripDriver{
		Id:         driver.Id,
		Name:       driver.Name,
		ProfilePic: driver.ProfilePic,
		CarPlate:   driver.CarPlate,
	})
	if err != nil {
		if errors.Is(err, repo.ErrOfferNotActive) {
			ack, marshalErr := json.Marshal(map[string]any{"command": contracts.DriverCmdTripAccept, "tripId": tripID, "status": "rejected", "reason": "offer_unavailable"})
			if marshalErr != nil {
				return marshalErr
			}
			return dc.bus.Producer.SendMessage(ctx, contracts.DriverEventCommandAcknowledged, &contracts.EventMessage{EntityID: driver.Id, Data: ack})
		}
		return err
	}
	logs.L().Infow("Trip accepted by driver", "tripID", tripID, "driverID", driver.Id)
	return nil
}
