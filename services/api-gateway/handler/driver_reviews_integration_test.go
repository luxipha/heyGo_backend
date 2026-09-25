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

func TestRiderCommentsVisibleToDriverAndAdminReviewQueue(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	adminID, riderID, driverID, otherDriverID, tripID, fareID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, adminID, "rating-review-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}, {otherDriverID, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, user.id, "rider-comment:"+user.id, "rider-comment-human:"+user.id, user.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name) VALUES($1::UUID,'Reviewed Driver')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,comment,admin_review_status) VALUES($1::UUID,$2::UUID,$3::UUID,1,'Please review this trip','pending')`, tripID, riderID, driverID); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	requestContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestContext.Request = httptest.NewRequest("GET", "/driver/reviews", nil)
	driverReviews, err := readDriverReceivedReviews(requestContext, pool, driverID, 25, nil)
	if err != nil || len(driverReviews) != 1 || driverReviews[0].Comment != "Please review this trip" || driverReviews[0].Rating != 1 {
		t.Fatalf("driver reviews = %+v, err=%v", driverReviews, err)
	}
	otherReviews, err := readDriverReceivedReviews(requestContext, pool, otherDriverID, 25, nil)
	if err != nil || len(otherReviews) != 0 {
		t.Fatalf("other driver's review read leaked data: %+v, err=%v", otherReviews, err)
	}

	api := &adminAPI{pool: pool}
	adminQueue := callAdminHandler(t, adminID, api.listDriverRatings, nil)
	if adminQueue.Code != 200 {
		t.Fatalf("admin queue status=%d body=%s", adminQueue.Code, adminQueue.Body.String())
	}
	var queue struct {
		Data []adminDriverRating `json:"data"`
	}
	if err := json.Unmarshal(adminQueue.Body.Bytes(), &queue); err != nil || len(queue.Data) != 1 || queue.Data[0].Comment != "Please review this trip" || queue.Data[0].DriverName != "Reviewed Driver" {
		t.Fatalf("admin queue = %+v err=%v", queue, err)
	}

	marked := callAdminHandler(t, adminID, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "tripID", Value: tripID}}
		api.markDriverRatingReviewed(c)
	}, nil)
	if marked.Code != 200 {
		t.Fatalf("mark reviewed status=%d body=%s", marked.Code, marked.Body.String())
	}
	var status string
	var reviewer *string
	if err := pool.QueryRow(ctx, `SELECT admin_review_status,admin_reviewed_by::TEXT FROM trip_ratings WHERE trip_id=$1::UUID AND actor_id=$2::UUID`, tripID, riderID).Scan(&status, &reviewer); err != nil {
		t.Fatal(err)
	}
	if status != "reviewed" || reviewer == nil || *reviewer != adminID {
		t.Fatalf("review state=%q reviewer=%v", status, reviewer)
	}
	if got := callAdminHandler(t, adminID, api.listDriverRatings, nil); got.Code != 200 || !json.Valid(got.Body.Bytes()) {
		t.Fatalf("pending queue after review status=%d body=%s", got.Code, got.Body.String())
	}
}
