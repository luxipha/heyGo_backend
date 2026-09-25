package repo

import (
	"context"
	"os"
	"testing"
	"time"

	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/google/uuid"
)

func TestCompletedTripAccruesDriverEarningsOnce(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 2, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, driver, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{rider, "rider"}, {driver, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1,$2,$3,ARRAY[$4]::TEXT[])`, item.id, "casper-"+item.id, "human-"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,available,online_requested) VALUES($1,'Test driver','TEST-1','sedan','on_trip',FALSE,FALSE)`, driver); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1,$2,'sedan',5000,'{}'::JSONB)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,started_at) VALUES($1,$2,$3,$4,'started',NOW())`, trip, rider, fare, driver); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if _, changed, err := repository.Complete(ctx, trip, driver); err != nil || !changed {
		t.Fatalf("complete: changed=%v err=%v", changed, err)
	}
	if _, changed, err := repository.Complete(ctx, trip, driver); err != nil || changed {
		t.Fatalf("repeat complete: changed=%v err=%v", changed, err)
	}
	var fareKobo, commissionKobo, earningsKobo int64
	var bps, count int
	if err := pool.QueryRow(ctx, `SELECT fare_kobo,commission_bps,commission_kobo,earnings_kobo,(SELECT COUNT(*) FROM driver_trip_earnings WHERE trip_id=$1::UUID) FROM driver_trip_earnings WHERE trip_id=$1::UUID`, trip).Scan(&fareKobo, &bps, &commissionKobo, &earningsKobo, &count); err != nil {
		t.Fatal(err)
	}
	if fareKobo != 5000 || bps != 0 || commissionKobo != 0 || earningsKobo != 5000 || count != 1 {
		t.Fatalf("unexpected accrual fare=%d bps=%d commission=%d earnings=%d count=%d", fareKobo, bps, commissionKobo, earningsKobo, count)
	}
}
