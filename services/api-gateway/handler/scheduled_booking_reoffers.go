package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

// Only the current replacement fare is offered. Older declines do not suppress
// a changed booking, but a decline made after this amendment still does.
func scheduledEditedOffersOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	for i := 0; i < limit; i++ {
		found, e := scheduledEditedOffer(ctx, pool)
		if e != nil {
			return e
		}
		if !found {
			return nil
		}
	}
	return nil
}

func scheduledEditedOffer(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var booking, request, driver string
	e = tx.QueryRow(ctx, `SELECT s.id::text,a.request_id::text,d.id::text FROM scheduled_rides s
 JOIN scheduled_ride_amendments a ON a.booking_id=s.id AND a.new_fare_id=s.fare_id
 JOIN drivers d ON scheduled_offer_eligible(d.id,s.id)
 WHERE s.status='open' AND s.dispatch_at>NOW()
 AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendment_notifications n WHERE n.booking_id=s.id AND n.request_id=a.request_id AND n.driver_id=d.id)
 AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendment_declines n WHERE n.booking_id=s.id AND n.request_id=a.request_id AND n.driver_id=d.id)
 ORDER BY s.pickup_at,s.id,d.id LIMIT 1 FOR UPDATE OF s SKIP LOCKED`).Scan(&booking, &request, &driver)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO scheduled_ride_amendment_notifications(booking_id,request_id,driver_id) VALUES($1::uuid,$2::uuid,$3::uuid) ON CONFLICT DO NOTHING`, booking, request, driver)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() == 1 {
		payload, _ := json.Marshal(map[string]any{"bookingId": booking, "audience": "driver", "refreshScheduledRides": true, "bookingChanged": true})
		_, e = messaging.AppendEventTx(ctx, tx, driver, "schedule-revised-offer:"+booking+":"+request, "scheduled.event.offer", payload)
		if e != nil {
			return false, e
		}
	}
	return true, tx.Commit(ctx)
}
