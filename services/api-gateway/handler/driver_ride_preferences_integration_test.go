package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDriverRidePreferencesOwnershipAndMatching(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['driver'])`, id); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		pool.Exec(ctx, `DELETE FROM trips WHERE id=$1::UUID`, trip)
		pool.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
		pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::UUID[])`, []string{owner, other})
	}()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, owner)
	exec(`INSERT INTO driver_vehicles(driver_id,package_slug,review_status) VALUES($1::UUID,'sedan','approved')`, owner)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{"routes":[{"distance":25000}]}')`, fare, other)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status,origin_region_code) VALUES($1::UUID,$2::UUID,$3::UUID,'pending','TEST_AREA')`, trip, other, fare)
	gin.SetMode(gin.TestMode)
	call := func(who, method, body string) *httptest.ResponseRecorder {
		r := gin.New()
		auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: who})
		registerDriverRoutes(r.Group("/", auth.Authenticate), pool, nil)
		req := httptest.NewRequest(method, "/driver/ride-preferences", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("%d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	matches := func(want bool) {
		t.Helper()
		var got bool
		if e := pool.QueryRow(ctx, `SELECT driver_ride_preference_matches($1::UUID,$2::UUID)`, owner, trip).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("matches=%v want=%v", got, want)
		}
	}
	matches(true)
	check(call(owner, "PUT", `{"version":0,"rideTypes":["xl"],"workingAreas":[],"longDistance":true,"airport":true}`), 400)
	check(call(owner, "PUT", `{"version":0,"rideTypes":[],"workingAreas":["NOT_A_REGION"],"longDistance":true,"airport":true}`), 400)
	body := `{"version":0,"rideTypes":["sedan"],"workingAreas":[],"longDistance":false,"airport":false,"autoAccept":true}`
	check(call(owner, "PUT", body), 200)
	check(call(owner, "PUT", body), 409)
	var result struct {
		Data struct {
			Preferences ridePreferences `json:"preferences"`
		} `json:"data"`
	}
	w := call(other, "GET", "")
	check(w, 200)
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if !result.Data.Preferences.AutoAccept || result.Data.Preferences.Version != 0 {
		t.Fatal("preferences leaked between owners")
	}
	matches(true) // exactly 25km allowed
	exec(`UPDATE ride_fares SET route='{"routes":[{"distance":25001}]}' WHERE id=$1::UUID`, fare)
	matches(false)
	exec(`UPDATE ride_fares SET route='{}' WHERE id=$1::UUID`, fare)
	matches(false)
	exec(`UPDATE ride_fares SET route='{"routes":[{"distance":1000}]}' WHERE id=$1::UUID`, fare)
	for _, tag := range []string{"AIRPORT_PICKUP", "AIRPORT_DROPOFF"} {
		exec(`UPDATE trips SET regulatory_tags=ARRAY[$2] WHERE id=$1::UUID`, trip, tag)
		matches(false)
	}
	exec(`UPDATE trips SET regulatory_tags='{}' WHERE id=$1::UUID`, trip)
	matches(true)
	exec(`UPDATE driver_ride_preferences SET working_areas=ARRAY['OTHER_AREA'] WHERE driver_id=$1::UUID`, owner)
	matches(false)
	exec(`UPDATE driver_ride_preferences SET working_areas=ARRAY['TEST_AREA'] WHERE driver_id=$1::UUID`, owner)
	matches(true)
	exec(`UPDATE trips SET origin_region_code=NULL WHERE id=$1::UUID`, trip)
	matches(false)
	exec(`UPDATE driver_ride_preferences SET working_areas='{}',ride_types=ARRAY['xl'] WHERE driver_id=$1::UUID`, owner)
	matches(false)
}
