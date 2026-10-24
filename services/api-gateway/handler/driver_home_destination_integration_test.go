package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHomeDestinationOwnershipVersionAndMatching(t *testing.T) {
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
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{owner, other} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['driver'])`, id)
	}
	defer func() {
		pool.Exec(ctx, `DELETE FROM trips WHERE id=$1::uuid`, trip)
		pool.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::uuid`, fare)
		pool.Exec(ctx, `DELETE FROM drivers WHERE id=$1::uuid`, owner)
		pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{owner, other})
	}()
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status,location) VALUES($1::uuid,'Test','TEST','sedan','offline',ST_SetSRID(ST_MakePoint(3.4,6.5),4326)::geography)`, owner)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,destination) VALUES($1::uuid,$2::uuid,'sedan',10000,'{}',ST_SetSRID(ST_MakePoint(3.49,6.5),4326)::geography)`, fare, other)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status,origin_region_code) VALUES($1::uuid,$2::uuid,$3::uuid,'pending','TEST')`, trip, other, fare)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schemaVersion":1,"state":"resolved","confirmationRequired":true,"candidates":[{"id":"home-place","name":"Confirmed home","address":"Lagos","provider":"google","location":{"latitude":6.5,"longitude":3.5}}],"place":{"id":"home-place","name":"Confirmed home","address":"Lagos","provider":"google","location":{"latitude":6.5,"longitude":3.5}}}`))
	}))
	defer server.Close()
	t.Setenv("RIDE_AGENT_URL", server.URL)
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("a", 48))
	t.Setenv("RIDE_AGENT_AUDIENCE", "")
	gin.SetMode(gin.TestMode)
	call := func(who, method, path, body string, intended int) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: who})
		registerDriverRoutes(r.Group("/", auth.Authenticate), pool, nil)
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != intended {
			t.Fatalf("%s: %d want %d: %s", path, w.Code, intended, w.Body.String())
		}
		return w
	}
	match := func(want bool) {
		t.Helper()
		var got bool
		if e := pool.QueryRow(ctx, `SELECT driver_home_matches($1::uuid,$2::uuid)`, owner, trip).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("home matches %v want %v", got, want)
		}
	}
	match(true)
	exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::uuid)`, owner)
	exec(`INSERT INTO driver_identity_profiles(driver_id,residential_address) VALUES($1::uuid,'12 Residential Street, Lagos')`, owner)
	residential := call(owner, "GET", "/driver/home-destination", "", 200)
	if !strings.Contains(residential.Body.String(), "12 Residential Street") {
		t.Fatal("Missing residential address")
	}
	call(owner, "PUT", "/driver/home-destination", `{"placeId":"home-place","version":0,"enabled":true}`, 200)
	match(true)
	exec(`UPDATE ride_fares SET destination=ST_SetSRID(ST_MakePoint(3.3,6.5),4326)::geography WHERE id=$1::uuid`, fare)
	match(false)
	exec(`UPDATE ride_fares SET destination=NULL WHERE id=$1::uuid`, fare)
	match(false)
	call(owner, "PATCH", "/driver/home-destination", `{"version":0,"enabled":false}`, 400)
	call(owner, "PUT", "/driver/home-destination", `{"placeId":"home-place","version":0,"enabled":true}`, 409)
	w := call(other, "GET", "/driver/home-destination", "", 200)
	if strings.Contains(w.Body.String(), "Confirmed home") || strings.Contains(w.Body.String(), "12 Residential Street") {
		t.Fatal("Home leaked across owners")
	}
	call(owner, "PATCH", "/driver/home-destination", `{"version":1,"enabled":false}`, 200)
	match(true)
	exec(`INSERT INTO driver_ride_preferences(driver_id,ride_types,auto_accept) VALUES($1::uuid,ARRAY['sedan'],false)`, owner)
	call(owner, "PATCH", "/driver/ride-preferences/auto-accept", `{"version":1,"enabled":true}`, 200)
	var preserved bool
	if e := pool.QueryRow(ctx, `SELECT auto_accept AND ride_types=ARRAY['sedan']::text[] FROM driver_ride_preferences WHERE driver_id=$1::uuid`, owner).Scan(&preserved); e != nil {
		t.Fatal(e)
	}
	if !preserved {
		t.Fatal("Auto changed unrelated preferences")
	}
	call(owner, "PATCH", "/driver/ride-preferences/auto-accept", `{"version":1,"enabled":false}`, 409)
	var residentialUnchanged string
	if err := pool.QueryRow(ctx, `SELECT residential_address FROM driver_identity_profiles WHERE driver_id=$1::uuid`, owner).Scan(&residentialUnchanged); err != nil || residentialUnchanged != "12 Residential Street, Lagos" {
		t.Fatal("Home changed identity address")
	}

}
