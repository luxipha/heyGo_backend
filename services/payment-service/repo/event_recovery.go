package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type PaymentEventPublisher interface {
	SendMessageAndWait(context.Context, string, *contracts.EventMessage, time.Duration) error
}

// DrainPaymentEvents holds a row lock until acknowledgement and commit. A lost
// acknowledgement or crash replays the stored envelope with the same event ID.
// Legacy rows without a recoverable envelope remain visible to backlog alerts.
func DrainPaymentEvents(ctx context.Context, pool *pgxpool.Pool, publisher PaymentEventPublisher, limit int) (int, error) {
	published := 0
	for published < limit {
		worked, err := publishPaymentEvent(ctx, pool, publisher)
		if err != nil || !worked {
			return published, err
		}
		published++
	}
	return published, nil
}

func publishPaymentEvent(ctx context.Context, pool *pgxpool.Pool, publisher PaymentEventPublisher) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('heygo_payment_event_recovery'))`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	var id, topic, entityID string
	var payload json.RawMessage
	err = tx.QueryRow(ctx, `SELECT event_id,topic,entity_id,payload FROM payment_webhook_events
		WHERE NOT published AND topic IS NOT NULL AND entity_id IS NOT NULL AND payload IS NOT NULL
		ORDER BY processed_at,event_id LIMIT 1 FOR UPDATE`).Scan(&id, &topic, &entityID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := publisher.SendMessageAndWait(deliveryCtx, topic, &contracts.EventMessage{
		EventID: id, EntityID: entityID, Data: payload,
	}, 10*time.Second); err != nil {
		return false, fmt.Errorf("publish recovered payment event: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_webhook_events SET published=TRUE WHERE event_id=$1`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
