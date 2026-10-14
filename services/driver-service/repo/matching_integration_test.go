package repo

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"os"
	"testing"
	"time"
)

func TestMatchingTerminalOutcomeIsDurable(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 1, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	rider := uuid.NewString()
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['rider'])`, rider)
	for _, outcome := range []string{"no_drivers", "timeout"} {
		fare, trip := uuid.NewString(), uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}')`, fare, rider)
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, trip, rider, fare)
		if err := finishMatching(ctx, tx, trip, outcome); err != nil {
			t.Fatal(err)
		}
		var status, actual string
		var cancelled *time.Time
		if err := tx.QueryRow(ctx, `SELECT status,matching_outcome,cancelled_at FROM trips WHERE id=$1::UUID`, trip).Scan(&status, &actual, &cancelled); err != nil {
			t.Fatal(err)
		}
		if status != "cancelled" || actual != outcome || cancelled == nil {
			t.Fatalf("terminal=%s/%s/%v", status, actual, cancelled)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM trip_event_outbox WHERE trip_id=$1::UUID AND topic=$2 AND recipient_id=$3::UUID`, trip, contracts.TripEventNoDriversFound, rider).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("durable outcome count=%d", count)
		}
	}
}

func TestMatchingNoDriverAndDeadlineWorkerClosePendingTrip(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['rider'])`, rider); err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Exec(ctx, `DELETE FROM trips WHERE rider_id=$1::UUID`, rider)
		pool.Exec(ctx, `DELETE FROM ride_fares WHERE rider_id=$1::UUID`, rider)
		pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, rider)
	}()
	r := NewPostgresDriverRepository(pool)
	for _, outcome := range []string{"no_drivers", "timeout"} {
		fare, trip := uuid.NewString(), uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}')`, fare, rider); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status,matching_deadline) VALUES($1::UUID,$2::UUID,$3::UUID,'pending',NOW()+CASE WHEN $4='timeout' THEN INTERVAL '-1 second' ELSE INTERVAL '2 minutes' END)`, trip, rider, fare, outcome); err != nil {
			t.Fatal(err)
		}
		if outcome == "no_drivers" {
			_, err = r.MatchAndReserve(ctx, trip, "sedan", 6.45, 3.39, 1, time.Second, []byte(`{}`))
			if !errors.Is(err, ErrNoAvailableDriver) {
				t.Fatalf("expected no driver: %v", err)
			}
		} else {
			if _, err := r.ExpireOffers(ctx, 50); err != nil {
				t.Fatal(err)
			}
		}
		var status, actual string
		if err := pool.QueryRow(ctx, `SELECT status,matching_outcome FROM trips WHERE id=$1::UUID`, trip).Scan(&status, &actual); err != nil {
			t.Fatal(err)
		}
		if status != "cancelled" || actual != outcome {
			t.Fatalf("worker status=%s outcome=%s", status, actual)
		}
	}
}
