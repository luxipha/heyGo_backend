package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	triprepo "github.com/luxipha/heyGo_backend/services/trip-service/repo"
	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/scheduling"
)

// RunScheduledRidesOnce is called by authenticated production maintenance or
// the local pull-mode worker. All state and events live in PostgreSQL.
func RunScheduledRidesOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	if e := scheduledRiderRemindersOnce(ctx, pool, limit); e != nil {
		return e
	}
	if e := scheduledOffersOnce(ctx, pool, limit); e != nil {
		return e
	}
	if e := scheduledEditedOffersOnce(ctx, pool, limit); e != nil {
		return e
	}
	rows, e := pool.Query(ctx, `SELECT id::text FROM scheduled_rides WHERE status IN('open','reserved') AND readiness_at<=NOW() AND (dispatch_at<=NOW() OR (driver_id IS NOT NULL AND reminder_sent_at IS NULL)) ORDER BY CASE WHEN pickup_at<NOW()-INTERVAL '5 minutes' THEN 0 ELSE 1 END,updated_at,dispatch_at,id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = maintainScheduledRide(ctx, pool, id); e != nil {
			return e
		}
	}
	return nil
}

// Pull-mode development has no Cloud Scheduler. The ticker is only a wake-up:
// persisted due times, atomic dispatch and idempotency handle process restarts.
func StartScheduledRideJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			e := RunScheduledRidesOnce(jobCtx, pool, 100)
			cancel()
			if e != nil && ctx.Err() == nil {
				slog.Warn("Scheduled ride worker failed", "error", e)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func scheduledOffersOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	rows, e := pool.Query(ctx, `SELECT s.id::text,d.id::text FROM scheduled_rides s JOIN drivers d ON scheduled_offer_eligible(d.id,s.id) WHERE s.status='open' AND s.dispatch_at>NOW() AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendments a WHERE a.booking_id=s.id AND a.new_fare_id=s.fare_id) AND NOT EXISTS(SELECT 1 FROM scheduled_ride_notifications n WHERE n.booking_id=s.id AND n.driver_id=d.id) AND NOT EXISTS(SELECT 1 FROM scheduled_ride_declines n WHERE n.booking_id=s.id AND n.driver_id=d.id) ORDER BY s.pickup_at,s.id,d.id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type pair struct{ booking, driver string }
	pairs := []pair{}
	for rows.Next() {
		var v pair
		if e = rows.Scan(&v.booking, &v.driver); e != nil {
			rows.Close()
			return e
		}
		pairs = append(pairs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range pairs {
		e = func() error {
			tx, e := pool.Begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback(ctx)
			var at time.Time
			if e = tx.QueryRow(ctx, `SELECT pickup_at FROM scheduled_rides WHERE id=$1::uuid AND status='open' AND dispatch_at>NOW() AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendments a WHERE a.booking_id=scheduled_rides.id AND a.new_fare_id=scheduled_rides.fare_id) AND scheduled_offer_eligible($2::uuid,id) FOR UPDATE SKIP LOCKED`, v.booking, v.driver).Scan(&at); errors.Is(e, pgx.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `INSERT INTO scheduled_ride_notifications(booking_id,driver_id)VALUES($1::uuid,$2::uuid)ON CONFLICT DO NOTHING`, v.booking, v.driver)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return nil
			}
			raw, _ := json.Marshal(map[string]any{"bookingId": v.booking, "pickupAt": at, "audience": "driver", "refreshScheduledRides": true})
			if _, e = messaging.AppendEventTx(ctx, tx, v.driver, "schedule-offer:"+v.booking, "scheduled.event.offer", raw); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}()
		if e != nil {
			return e
		}
	}
	return nil
}
func maintainScheduledRide(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var fareID, owner string
	var shouldDispatch bool
	e := func() error {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SELECT u.id FROM users u JOIN scheduled_rides s ON s.rider_id=u.id WHERE s.id=$1::uuid FOR SHARE OF u`, id); e != nil {
			return e
		}
		var state string
		var driver *string
		var ready, reminder *time.Time
		var pickup, dispatch time.Time
		e = tx.QueryRow(ctx, `SELECT fare_id::text,rider_id::text,status,driver_id::text,ready_at,reminder_sent_at,pickup_at,dispatch_at FROM scheduled_rides WHERE id=$1::uuid AND status IN('open','reserved') FOR UPDATE SKIP LOCKED`, id).Scan(&fareID, &owner, &state, &driver, &ready, &reminder, &pickup, &dispatch)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		// Rotate unsuccessful supply checks through the bounded batch so that
		// early bookings without drivers cannot starve later due bookings.
		if _, e = tx.Exec(ctx, `UPDATE scheduled_rides SET updated_at=NOW() WHERE id=$1::uuid`, id); e != nil {
			return e
		}
		if time.Now().After(pickup.Add(5 * time.Minute)) {
			_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET status='unfulfilled',reason='pickup_window_missed',version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id)
			if e == nil {
				e = scheduleEvent(ctx, tx, id, "scheduled.event.unfulfilled")
			}
			if e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		if dispatch.After(time.Now()) {
			if driver != nil && reminder == nil {
				_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET reminder_sent_at=NOW(),version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id)
				if e == nil {
					e = scheduleEvent(ctx, tx, id, "scheduled.event.reminder")
				}
				if e != nil {
					return e
				}
			}
			return tx.Commit(ctx)
		}
		// Readiness isn't an exemption from fresh online/category/balance checks.
		var viable bool
		if driver != nil && ready != nil {
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM drivers d JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.id=$1::uuid AND scheduled_offer_eligible(d.id,$2::uuid) AND d.online_requested AND d.available AND d.status='available' AND l.recorded_at>=NOW()-INTERVAL '2 minutes' AND EXISTS(SELECT 1 FROM driver_socket_sessions ds WHERE ds.driver_id=d.id AND ds.expires_at>NOW()) AND NOT EXISTS(SELECT 1 FROM trips WHERE assigned_driver_id=d.id AND status IN('assigned','accepted','arrived','started')))`, *driver, id).Scan(&viable)
			if e != nil {
				return e
			}
		}
		if driver != nil && !viable {
			_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET status='open',driver_id=NULL,ready_at=NULL,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id)
			if e == nil {
				e = scheduleEvent(ctx, tx, id, "scheduled.event.replacement_required", *driver)
			}
			if e != nil {
				return e
			}
		}
		if viable {
			shouldDispatch = true
		} else {
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scheduled_rides s JOIN ride_fares f ON f.id=s.fare_id JOIN drivers d ON true JOIN driver_eligibility e ON e.driver_id=d.id JOIN driver_live_locations l ON l.driver_id=d.id WHERE s.id=$1::uuid AND `+driverstate.AvailabilityPredicate("s.market_code", "f.package_slug", "f.pickup", "15000")+`)`, id).Scan(&shouldDispatch)
			if e != nil {
				return e
			}
		}
		return tx.Commit(ctx)
	}()
	if e != nil || !shouldDispatch {
		return e
	}
	// Create persists the booking-to-trip link and normal trip outbox together.
	// Retries recover consumed_trip_id instead of creating another active ride.
	repository := triprepo.NewPostgresRepository(pool)
	fare, e := repository.GetRideFareByID(ctx, fareID)
	if e != nil {
		return e
	}
	trip, e := repository.Create(scheduling.DispatchContext(ctx, id), &triptypes.TripModel{ID: uuid.NewString(), RiderID: owner, Status: "pending", RideFare: fare})
	if e != nil {
		if errors.Is(e, triprepo.ErrActiveRiderTrip) || errors.Is(e, triprepo.ErrScheduledConflict) {
			return nil
		}
		if !errors.Is(e, triprepo.ErrPricingUnavailable) && !errors.Is(e, triprepo.ErrDebtChanged) && !errors.Is(e, triprepo.ErrRiderRestricted) && !errors.Is(e, triprepo.ErrFareExpired) && !errors.Is(e, triprepo.ErrFareInvalid) {
			return fmt.Errorf("scheduled dispatch: %w", e)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, `UPDATE scheduled_rides SET status='unfulfilled',reason='dispatch_precondition_changed',version=version+1,updated_at=NOW() WHERE id=$1::uuid AND status IN('open','reserved')`, id)
		if err == nil && tag.RowsAffected() > 0 {
			err = scheduleEvent(ctx, tx, id, "scheduled.event.unfulfilled")
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	_ = trip // Existing trip-created outbox drives dispatch and participant recovery.
	return nil
}
