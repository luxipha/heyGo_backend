package handler

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/google/uuid"
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
