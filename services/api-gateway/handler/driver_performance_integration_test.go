package handler

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestDriverPerformanceUsesAllTimePeerRatingPercentile(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	riderID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, riderID, "performance-rider:"+riderID, "performance-rider:"+riderID); err != nil {
		t.Fatal(err)
	}
	drivers := make([]string, 20)
	for i := range drivers {
		driverID := uuid.NewString()
		drivers[i] = driverID
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "performance-driver:"+driverID, "performance-driver:"+driverID); err != nil {
			t.Fatal(err)
		}
		for tripIndex := 0; tripIndex < driverPerformanceMinimumRatings; tripIndex++ {
			fareID, tripID := uuid.NewString(), uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fareID, riderID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, tripID, riderID, fareID, driverID); err != nil {
				t.Fatal(err)
			}
			rating := 4
			if i == 0 {
				rating = 5
			}
			if _, err := pool.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating) VALUES($1::UUID,$2::UUID,$3::UUID,$4)`, tripID, riderID, driverID, rating); err != nil {
				t.Fatal(err)
			}
		}
	}
	performance, err := readDriverPerformance(ctx, pool, drivers[0])
	if err != nil {
		t.Fatal(err)
	}
	expectedTopPercent := int(math.Ceil(100.0 / float64(performance.RatedDriverCohortCount)))
	if performance.RatingAverage == nil || *performance.RatingAverage != 5 || performance.RatingCount != 25 || !performance.PercentileEligible || performance.TopPercent == nil || *performance.TopPercent != expectedTopPercent || performance.RatedDriverCohortCount < 20 || performance.CompletedTrips != 25 {
		t.Fatalf("unexpected performance summary: %+v", performance)
	}
	newDriver := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, newDriver, "performance-new:"+newDriver, "performance-new:"+newDriver); err != nil {
		t.Fatal(err)
	}
	withoutRatings, err := readDriverPerformance(ctx, pool, newDriver)
	if err != nil {
		t.Fatal(err)
	}
	if withoutRatings.RatingAverage != nil || withoutRatings.RatingCount != 0 || withoutRatings.PercentileEligible || withoutRatings.TopPercent != nil || withoutRatings.RatedDriverCohortCount != 0 {
		t.Fatalf("new driver should not receive a rating rank: %+v", withoutRatings)
	}
}

func TestDriverStandingCountsOnlyTheDriversOwnDecisions(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	riderID, driverID := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$2,ARRAY['rider'])`, riderID, "standing-rider:"+riderID)
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,provider_trust_score,provider_trust_status) VALUES($1::UUID,$2,$2,ARRAY['driver'],720,'trusted')`, driverID, "standing-driver:"+driverID)
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Standing','TEST','sedan','offline')`, driverID)
	offer := func(assignment, tripStatus string, assigned bool, driverCancelled bool) {
		t.Helper()
		fareID, tripID := uuid.NewString(), uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fareID, riderID)
		var assignee, actor *string
		if assigned {
			assignee = &driverID
		}
		if driverCancelled {
			actor = &driverID
		}
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,cancellation_actor_id) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6::UUID)`, tripID, riderID, fareID, assignee, tripStatus, actor)
		exec(`INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,trip_payload) VALUES($1::UUID,$2::UUID,1,$3,NOW(),'{}'::JSONB)`, tripID, driverID, assignment)
	}
	offer("completed", "completed", true, false)
	offer("accepted", "accepted", true, false)
	offer("cancelled", "cancelled", true, true) // accepted, then the driver cancelled
	offer("declined", "pending", false, false)
	offer("timed_out", "pending", false, false)
	offer("cancelled", "cancelled", false, false) // rider cancelled while the offer was open

	got, err := readDriverPerformance(ctx, pool, driverID)
	if err != nil {
		t.Fatal(err)
	}
	o := got.Offers
	if o.Accepted != 3 || o.Declined != 1 || o.TimedOut != 1 || o.Decided != 5 || o.PickRatePercent == nil || *o.PickRatePercent != 60 || o.IgnoreRatePercent == nil || *o.IgnoreRatePercent != 20 {
		t.Fatalf("offers: %+v", o)
	}
	if got.Trips.Assigned != 3 || got.Trips.DriverCancelled != 1 || got.Trips.CancellationRatePercent == nil || *got.Trips.CancellationRatePercent != 33 {
		t.Fatalf("trips: %+v", got.Trips)
	}
	if got.Trust.Score == nil || *got.Trust.Score != 720 || got.Trust.Status == nil || *got.Trust.Status != "trusted" {
		t.Fatalf("trust: %+v", got.Trust)
	}
	if got.WindowDays != 30 || got.PercentileEligible || got.PercentileUnavailableReason != "ratings" || got.TopPercent != nil {
		t.Fatalf("percentile: %+v", got)
	}
}
