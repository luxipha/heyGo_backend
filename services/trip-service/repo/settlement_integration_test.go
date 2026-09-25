package repo

import (
	"context"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/google/uuid"
)

func TestCompleteCreatesPendingSettlementAndDurableEvents(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	riderID, driverID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	// Keep immutable settlement action history intact. Fixtures use fresh UUIDs
	// on every run and this test is intended for a disposable integration DB.
	for _, u := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "settlement-test:"+u.id, "settlement-test:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Test Driver','TEST-1','sedan','on_trip')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',1250.50,'{"routes":[]}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'accepted')`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}

	repository := NewPostgresRepository(pool)
	trip, changed, err := repository.Arrive(ctx, tripID, driverID)
	if err != nil || !changed || trip.Status != "arrived" {
		t.Fatalf("arrival trip=%+v changed=%v err=%v", trip, changed, err)
	}
	trip, changed, err = repository.Arrive(ctx, tripID, driverID)
	if err != nil || changed || trip.Status != "arrived" {
		t.Fatalf("repeat arrival trip=%+v changed=%v err=%v", trip, changed, err)
	}
	var arrivedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT arrived_at FROM trips WHERE id=$1::UUID`, tripID).Scan(&arrivedAt); err != nil || arrivedAt == nil {
		t.Fatalf("persisted arrived_at=%v err=%v", arrivedAt, err)
	}
	trip, changed, err = repository.Start(ctx, tripID, driverID)
	if err != nil || !changed || trip.Status != "started" {
		t.Fatalf("start after arrival trip=%+v changed=%v err=%v", trip, changed, err)
	}
	trip, changed, err = repository.Complete(ctx, tripID, driverID)
	if err != nil || !changed || trip.Status != "completed" {
		t.Fatalf("complete trip=%+v changed=%v err=%v", trip, changed, err)
	}
	var status string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT status,expected_amount_kobo FROM trip_settlements WHERE trip_id=$1::UUID`, tripID).Scan(&status, &amount); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || amount != 1251 {
		t.Fatalf("settlement status=%s expected amount=%d; want pending, 1251", status, amount)
	}
	var pendingActions, pendingEvents int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_settlement_actions WHERE trip_id=$1::UUID AND action='pending'`, tripID).Scan(&pendingActions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_event_outbox WHERE trip_id=$1::UUID AND topic='trip.event.settlement_updated' AND attempt=0`, tripID).Scan(&pendingEvents); err != nil {
		t.Fatal(err)
	}
	if pendingActions != 1 || pendingEvents != 2 {
		t.Fatalf("pending action rows=%d settlement outbox recipients=%d; want 1 and 2", pendingActions, pendingEvents)
	}
	if _, changed, err := repository.Complete(ctx, tripID, driverID); err != nil || changed {
		t.Fatalf("duplicate completion changed=%v err=%v", changed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_settlement_actions WHERE trip_id=$1::UUID AND action='pending'`, tripID).Scan(&pendingActions); err != nil {
		t.Fatal(err)
	}
	if pendingActions != 1 {
		t.Fatalf("duplicate completion created %d pending actions", pendingActions)
	}
	if _, err := pool.Exec(ctx, `UPDATE trip_event_outbox SET published_at=NOW() WHERE trip_id=$1::UUID AND published_at IS NULL`, tripID); err != nil {
		t.Fatalf("mark test outbox rows drained: %v", err)
	}
}
