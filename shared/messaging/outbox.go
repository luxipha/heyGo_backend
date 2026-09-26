package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type outboxPublisher interface {
	SendMessageAndWait(context.Context, string, *contracts.EventMessage, time.Duration) error
}

// PublishOutbox serializes publishers across backend replicas. The lock
// remains held until Pub/Sub acknowledges and the row is marked published. A
// crash between those actions replays the same event ID (at least once).
func PublishOutbox(ctx context.Context, pool *pgxpool.Pool, producer outboxPublisher) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		published, err := publishNext(ctx, pool, producer)
		if err != nil && ctx.Err() == nil {
			log.Printf("trip outbox delivery failed: %v", err)
		}
		if published && err == nil {
			continue // Drain backlog without a per-event polling delay.
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func publishNext(ctx context.Context, pool *pgxpool.Pool, producer outboxPublisher) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('heygo_trip_outbox'))`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	var id int64
	var tripID, topic, recipient, correlationID string
	var payload json.RawMessage
	err = tx.QueryRow(ctx, `SELECT id,trip_id::TEXT,topic,recipient_id::TEXT,payload,COALESCE(correlation_id,'') FROM trip_event_outbox
		WHERE published_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE`).Scan(&id, &tripID, &topic, &recipient, &payload, &correlationID)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	message := &contracts.EventMessage{EventID: fmt.Sprintf("trip-outbox-%d", id), CorrelationID: correlationID, EntityID: recipient, Data: payload}
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := producer.SendMessageAndWait(deliveryCtx, topic, message, 10*time.Second); err != nil {
		return false, fmt.Errorf("publish %s for trip %s: %w", topic, tripID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE trip_event_outbox SET published_at=NOW() WHERE id=$1`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
