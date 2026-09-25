package repo

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/google/uuid"
)

func TestRatePersistsFeedbackTagsOnce(t *testing.T) {
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
	rider, driver, trip, fare := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, user := range []struct{ id, role string }{{rider, "rider"}, {driver, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, user.id, "rating:"+user.id, "rating-human:"+user.id, user.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, trip, rider, fare, driver); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	tags := []string{"clean_and_tidy", "easy_pickup"}
	subject, created, err := repository.Rate(ctx, trip, driver, 5, tags, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || subject.Role != "consumer" || subject.CasperID != "rating:"+rider {
		t.Fatalf("unexpected rating result: %+v created=%v", subject, created)
	}
	_, created, err = repository.Rate(ctx, trip, driver, 3, []string{"great_chat"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("replayed rating must not overwrite the original")
	}
	var rating int
	var savedTags []string
	if err := pool.QueryRow(ctx, `SELECT rating,feedback_tags FROM trip_ratings WHERE trip_id=$1::UUID AND actor_id=$2::UUID`, trip, driver).Scan(&rating, &savedTags); err != nil {
		t.Fatal(err)
	}
	if rating != 5 || !reflect.DeepEqual(savedTags, tags) {
		t.Fatalf("rating persisted as %d with tags %v", rating, savedTags)
	}
	if _, _, err := repository.Rate(ctx, trip, driver, 5, nil, "Driver comment is not allowed"); err == nil {
		t.Fatal("driver-authored written comment was accepted")
	}
	riderSubject, created, err := repository.Rate(ctx, trip, rider, 1, nil, "Driver arrived promptly.")
	if err != nil || !created || riderSubject.Role != "provider" || riderSubject.CasperID != "rating:"+driver {
		t.Fatalf("rider review result = %+v created=%v err=%v", riderSubject, created, err)
	}
	var riderComment, reviewStatus string
	if err := pool.QueryRow(ctx, `SELECT comment,admin_review_status FROM trip_ratings WHERE trip_id=$1::UUID AND actor_id=$2::UUID`, trip, rider).Scan(&riderComment, &reviewStatus); err != nil {
		t.Fatal(err)
	}
	if riderComment != "Driver arrived promptly." || reviewStatus != "pending" {
		t.Fatalf("rider comment/status = %q/%q", riderComment, reviewStatus)
	}
	if _, _, err := repository.Rate(ctx, trip, rider, 4, nil, strings.Repeat("x", 251)); err == nil {
		t.Fatal("expected overlong rider comment to fail")
	}
	badTagsTrip, badTagsFare := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, badTagsFare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, badTagsTrip, rider, badTagsFare, driver); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,feedback_tags) VALUES($1::UUID,$2::UUID,$3::UUID,4,ARRAY['unsupported'])`, badTagsTrip, driver, rider); err == nil {
		t.Fatal("database accepted an unsupported feedback tag")
	}
}
