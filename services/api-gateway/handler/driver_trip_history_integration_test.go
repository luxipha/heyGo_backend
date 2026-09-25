package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestDriverTripHistoryPaginationAndReceipt(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driver, rider, other, completedTrip, cancelledTrip, completedFare, cancelledFare := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{driver, "driver"}, {rider, "rider"}, {other, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, item.id, "trip-history:"+item.id, "history-human:"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'History Driver','TEST','sedan','offline'),($2::UUID,'Other Driver','TEST2','sedan','offline')`, driver, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$3::UUID,'sedan',500000,'{"distanceMeters":1200}'::JSONB),($2::UUID,$3::UUID,'sedan',700000,'{"distanceMeters":2400}'::JSONB)`, completedFare, cancelledFare, rider); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,created_at,updated_at,started_at,completed_at)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed',$5::TIMESTAMPTZ-INTERVAL '1 hour',$5::TIMESTAMPTZ-INTERVAL '10 minutes',$5::TIMESTAMPTZ-INTERVAL '30 minutes',$5::TIMESTAMPTZ-INTERVAL '10 minutes')`, completedTrip, rider, completedFare, driver, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,cancellation_reason,cancellation_actor_id,created_at,updated_at,cancelled_at)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'cancelled','rider_requested_cancellation',$2::UUID,$5::TIMESTAMPTZ-INTERVAL '2 hours',$5::TIMESTAMPTZ-INTERVAL '1 hour',$5::TIMESTAMPTZ-INTERVAL '1 hour')`, cancelledTrip, rider, cancelledFare, driver, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status,resolved_at) VALUES($1::UUID,$2::UUID,$3::UUID,500000,'confirmed',$4)`, completedTrip, driver, rider, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,feedback_tags) VALUES($1::UUID,$2::UUID,$3::UUID,5,ARRAY['clean_and_tidy','easy_pickup'])`, completedTrip, driver, rider); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	gctx, _ := gin.CreateTestContext(nil)
	gctx.Request = httptest.NewRequest("GET", "/driver/trips", nil)
	first, err := readDriverTripHistory(gctx, pool, driver, 1, nil, nil, nil, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("expected limit+1 rows for pagination, got %d", len(first))
	}
	if first[0].ID != completedTrip || first[0].SettlementStatus == nil || *first[0].SettlementStatus != "confirmed" || first[0].Rating == nil || *first[0].Rating != 5 || len(first[0].FeedbackTags) != 2 {
		t.Fatalf("unexpected completed trip history: %+v", first[0])
	}
	cursor := driverTripHistoryCursor{OccurredAt: first[0].OccurredAt, ID: first[0].ID}
	encoded := encodeDriverTripHistoryCursor(cursor)
	decoded, err := decodeDriverTripHistoryCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readDriverTripHistory(gctx, pool, driver, 1, &decoded, nil, nil, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != cancelledTrip || second[0].CancelledBy == nil || *second[0].CancelledBy != "rider" {
		t.Fatalf("unexpected second history page: %+v", second)
	}
	receipt, err := readDriverTripReceipt(gctx, pool, driver, completedTrip)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TripID != completedTrip || receipt.ReceiptReference != "trip:"+completedTrip || receipt.PaymentStatus != "confirmed" || receipt.ConfirmationSource != "driver_entered" || receipt.FareKobo != 500000 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if _, err := json.Marshal(receipt); err != nil {
		t.Fatal(err)
	}
	otherPage, err := readDriverTripHistory(gctx, pool, other, 1, nil, nil, nil, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(otherPage) != 0 {
		t.Fatalf("history leaked across drivers: %+v", otherPage)
	}
}
