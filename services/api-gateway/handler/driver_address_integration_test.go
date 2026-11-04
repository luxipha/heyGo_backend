package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestDriverTripAddresses(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driver, rider, other, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, u := range []struct{ id, role string }{{driver, "driver"}, {rider, "rider"}, {other, "driver"}} {
		_, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles)VALUES($1::uuid,$1,$1,ARRAY[$2])`, u.id, u.role)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup_metadata,destination_metadata)VALUES($1::uuid,$2::uuid,'sedan',100000,'{}','{"name":"Yaba","address":"Yaba, Lagos"}','{"name":"Ikeja","address":"Ikeja, Lagos"}')`, fare, rider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'accepted')`, trip, rider, fare, driver)
	if err != nil {
		t.Fatal(err)
	}
	api := &driverAPI{pool: pool}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		id := driver
		if c.GetHeader("X-Other") == "true" {
			id = other
		}
		c.Set("authenticated-user", gatewayauth.User{ID: id})
	})
	router.GET("/active", api.activeTrip)
	router.GET("/trips/:tripID", api.tripDetail)
	for _, path := range []string{"/active", "/trips/" + trip} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != 200 {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data struct {
				PickupMetadata      map[string]any `json:"pickupMetadata"`
				DestinationMetadata map[string]any `json:"destinationMetadata"`
			} `json:"data"`
		}
		if err = json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.PickupMetadata["address"] != "Yaba, Lagos" || body.Data.DestinationMetadata["address"] != "Ikeja, Lagos" {
			t.Fatalf("addresses omitted: %s", recorder.Body.String())
		}
	}
	request := httptest.NewRequest("GET", "/trips/"+trip, nil)
	request.Header.Set("X-Other", "true")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != 404 {
		t.Fatalf("unassigned Driver received trip addresses: %d", recorder.Code)
	}
}
