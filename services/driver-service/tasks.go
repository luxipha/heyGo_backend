package main

import (
	"context"
	"fmt"
	"time"

	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/tasks"
)

type cloudTaskOfferScheduler struct {
	client *tasks.Client
}

func (s cloudTaskOfferScheduler) ScheduleOfferExpiry(ctx context.Context, tripID string, attempt int, at time.Time) error {
	origin, ok := pubsub.PushOrigin(ctx)
	if !ok {
		return fmt.Errorf("Pub/Sub push origin is unavailable")
	}
	key := fmt.Sprintf("offer-expiry:%s:%d", tripID, attempt)
	return s.client.ScheduleJSON(ctx, key, origin, "/internal/tasks/offers/expire", at, map[string]any{
		"tripId":  tripID,
		"attempt": attempt,
	})
}
