package trust

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Worker struct {
	pool     *pgxpool.Pool
	reporter Reporter
}

func NewWorker(pool *pgxpool.Pool, reporter Reporter) *Worker {
	return &Worker{pool: pool, reporter: reporter}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.DrainOnce(ctx, 25); err != nil && ctx.Err() == nil {
			// The next pass retries the durable row; a transient CasperID failure
			// must not fail the trip service process.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) DrainOnce(ctx context.Context, limit int) error {
	for i := 0; i < limit; i++ {
		worked, err := w.deliverOne(ctx)
		if err != nil {
			return err
		}
		if !worked {
			return nil
		}
	}
	return nil
}

func (w *Worker) deliverOne(ctx context.Context) (bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var event Event
	err = tx.QueryRow(ctx, `
		UPDATE casperid_trust_deliveries SET attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '10 minutes'
		WHERE id=(SELECT id FROM casperid_trust_deliveries WHERE status='pending' AND next_attempt_at<=NOW() ORDER BY id FOR UPDATE SKIP LOCKED)
		RETURNING id,casper_id_user_id,actor_role,event_type,external_event_id,interaction_reference,rating,occurred_at`,
	).Scan(&id, &event.UserID, &event.ActorRole, &event.EventType, &event.ExternalEventID, &event.InteractionReference, &event.Rating, &event.OccurredAt)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	if err := w.reporter.Submit(ctx, event); err != nil {
		delay := retryDelay()
		_, updateErr := w.pool.Exec(ctx, `UPDATE casperid_trust_deliveries SET next_attempt_at=NOW()+make_interval(secs=>$2),last_error=$3 WHERE id=$1 AND status='pending'`, id, int64(delay.Seconds()), err.Error())
		if updateErr != nil {
			return true, fmt.Errorf("record CasperID trust retry: %w (original: %v)", updateErr, err)
		}
		return true, nil
	}
	if _, err := w.pool.Exec(ctx, `UPDATE casperid_trust_deliveries SET status='delivered',delivered_at=NOW(),last_error='' WHERE id=$1 AND status='pending'`, id); err != nil {
		return true, fmt.Errorf("mark CasperID trust event delivered: %w", err)
	}
	return true, nil
}

func retryDelay() time.Duration {
	// The delivery row's attempt count is persisted; this bounded delay keeps
	// failures from hot-looping while preserving eventual delivery.
	return 10 * time.Minute
}
