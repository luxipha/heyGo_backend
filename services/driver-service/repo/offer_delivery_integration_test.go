package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

func TestDriverOfferSocketEventIsAtomicAndReplaySafe(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", commit), func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			rider, driver, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			defer func() {
				pool.Exec(context.Background(), `DELETE FROM trips WHERE id=$1::uuid`, trip)
				pool.Exec(context.Background(), `DELETE FROM ride_fares WHERE id=$1::uuid`, fare)
				pool.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1::uuid,$2::uuid)`, rider, driver)
			}()
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := tx.Exec(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['rider']),($2::uuid,$2,$2,ARRAY['driver'])`, rider, driver)
			exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
			exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::uuid,$2::uuid,$3::uuid,'pending')`, trip, rider, fare)
			offer, _ := json.Marshal(map[string]any{"trip": map[string]any{"id": trip}, "offer": map[string]any{"attempt": 1, "expiresAt": time.Now().Add(20 * time.Second)}})
			if err := enqueueDriverOffer(ctx, tx, trip, driver, offer); err != nil {
				t.Fatal(err)
			}
			// A retry and later broker replay share the exact source identity.
			if err := enqueueDriverOffer(ctx, tx, trip, driver, offer); err != nil {
				t.Fatal(err)
			}
			var outboxID int64
			if err := tx.QueryRow(ctx, `SELECT id FROM trip_event_outbox WHERE trip_id=$1::uuid AND topic=$2`, trip, contracts.DriverCmdTripRequest).Scan(&outboxID); err != nil {
				t.Fatal(err)
			}
			if _, err := messaging.AppendEventTx(ctx, tx, driver, fmt.Sprintf("trip-outbox-%d", outboxID), contracts.DriverCmdTripRequest, offer); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type=$2`, driver, contracts.DriverCmdTripRequest).Scan(&count); err != nil || count != 1 {
				t.Fatalf("offer events=%d err=%v", count, err)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM driver_notifications WHERE driver_id=$1::uuid`, driver).Scan(&count); err != nil || count != 1 {
				t.Fatalf("notifications=%d err=%v", count, err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid`, driver).Scan(&count); err != nil || count != 0 {
				t.Fatalf("uncommitted event visible=%d err=%v", count, err)
			}
			if commit {
				err = tx.Commit(ctx)
			} else {
				err = tx.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if commit {
				want = 1
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type=$2`, driver, contracts.DriverCmdTripRequest).Scan(&count); err != nil || count != want {
				t.Fatalf("visible offer events=%d want=%d err=%v", count, want, err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM trip_event_outbox WHERE trip_id=$1::uuid AND published_at IS NULL`, trip).Scan(&count); err != nil || count != want {
				t.Fatalf("pending outbox=%d want=%d err=%v", count, want, err)
			}
		})
	}
}
