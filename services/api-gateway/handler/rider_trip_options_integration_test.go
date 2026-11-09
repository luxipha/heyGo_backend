package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	triprepo "github.com/luxipha/heyGo_backend/services/trip-service/repo"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"github.com/luxipha/heyGo_backend/shared/tripsafety"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type optionsTestRouter struct{}

func (optionsTestRouter) Route(_ context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
	return &geo.RouteResponse{Provider: "test", Routes: []geo.Route{{Distance: 1000, Duration: 100, Geometry: geo.Geometry{Coordinates: [][]float64{{a.Longitude, a.Latitude}, {b.Longitude, b.Latitude}}}}}}, nil
}

func TestRiderTripOptionsAuthoritativeContracts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, other, driver := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other, driver} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles)VALUES($1::uuid,$1,$1,ARRAY['rider','driver'])`, id)
	}
	creator, approver := uuid.NewString(), uuid.NewString()
	for _, id := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'unused')`, id)
	}
	market := "OPTIONS_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=NOW() WHERE code=$1 AND effective_until IS NULL`, market)
	})
	pricing := uuid.NewString()
	for _, kind := range []string{"market", "region"} {
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,$3,$3,1,ST_GeomFromText('MULTIPOLYGON(((3.8 6.8,4 6.8,4 7,3.8 7,3.8 6.8)))',4326),'approved',NOW()-INTERVAL '1 day',$4::uuid,$5::uuid,NOW())`, uuid.NewString(), kind, market, creator, approver)
	}
	exec(`INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,1,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW())`, pricing, market, creator, approver)
	exec(`INSERT INTO pricing_package_rates(pricing_id,package_slug,base_kobo,per_km_kobo,per_minute_kobo)VALUES($1::uuid,'sedan',1000,1000,100)`, pricing)
	fare, trip := uuid.NewString(), uuid.NewString()
	originalDestination := agent.Place{ID: "original", Provider: "google", Name: "Old destination", Address: "Old address", Location: agent.Coordinate{Latitude: 6.94, Longitude: 3.94}}
	destRaw, _ := json.Marshal(originalDestination)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,destination_metadata,pricing_version_id,pricing_market_code)VALUES($1::uuid,$2::uuid,'sedan',10000,'{"routes":[{"distance":1000,"duration":100,"geometry":{"coordinates":[[3.9,6.9],[3.94,6.94]]}}]}',ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,ST_SetSRID(ST_MakePoint(3.94,6.94),4326)::geography,$3,$4::uuid,$5)`, fare, owner, destRaw, pricing, market)
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status)VALUES($1::uuid,'Driver','TEST','sedan','on_trip')`, driver)
	exec(`INSERT INTO driver_live_locations(driver_id,location,recorded_at)VALUES($1::uuid,ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,NOW())`, driver)
	router := gin.New()
	g := router.Group("", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Owner"), Roles: []string{"rider", "driver"}})
	})
	registerRiderTripOptions(g, pool)
	driverAPI := &driverAPI{pool: pool}
	g.GET("/test/trips/:tripID/contact", driverAPI.tripRiderContact)
	g.GET("/test/trips/:tripID/details", driverAPI.tripDetail)
	api := &riderAmendmentAPI{pool: pool, router: optionsTestRouter{}}
	g.POST("/test/trips/:tripID/amendments/preview", api.preview)
	call := func(who, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Owner", who)
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var reply struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &reply) != nil {
			t.Fatal(w.Body.String())
		}
		return reply.Data
	}
	notePath := "/rider/fares/" + fare + "/pickup-note"
	if w := call(owner, "PUT", notePath, `{"pickupNote":"  Meet at the blue gate  "}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(other, "PUT", notePath, `{"pickupNote":"foreign"}`); w.Code != 409 {
		t.Fatal("foreign note edit allowed")
	}
	if w := call(owner, "PUT", notePath, `{"pickupNote":"bad\u0000note"}`); w.Code != 400 {
		t.Fatal("control characters accepted")
	}
	guestPath := "/rider/fares/" + fare + "/guest"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{owner, `{"name":"Amina","phone":"08012345678"}`, 400},
		{other, `{"name":"Amina","phone":"+2348012345678"}`, 409},
		{owner, `{"name":" Amina ","phone":"+2348012345678"}`, 200},
	} {
		if w := call(tc.who, "PUT", guestPath, tc.body); w.Code != tc.code {
			t.Fatal("guest update", w.Code, w.Body.String())
		}
	}
	prefPath := "/rider/fares/" + fare + "/preferences"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{owner, `{"preferences":{"conversation":"quiet"}}`, 400},
		{other, `{"preferences":{"conversation":"quiet","air":"ac_on","music":"off"}}`, 409},
		{owner, `{"preferences":{"conversation":"quiet","air":"ac_on","music":"off"}}`, 200},
	} {
		if w := call(tc.who, "PUT", prefPath, tc.body); w.Code != tc.code {
			t.Fatal("fare preferences", w.Code, w.Body.String())
		}
	}
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,market_code,origin_region_code)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'accepted',$5,$5)`, trip, owner, driver, fare, market)
	exec(`UPDATE ride_fares SET consumed_trip_id=$2::uuid,consumed_at=NOW() WHERE id=$1::uuid`, fare, trip)
	var savedPreferences string
	if e := pool.QueryRow(ctx, `SELECT ride_preferences->>'conversation' FROM trips WHERE id=$1::uuid`, trip).Scan(&savedPreferences); e != nil || savedPreferences != "quiet" {
		t.Fatal("preferences lost at booking", e)
	}
	updatePath := "/rider/trips/" + trip + "/preferences"
	updateBody := `{"expectedRevision":0,"preferences":{"conversation":"happy_to_chat","air":"windows_down","music":"low"}}`
	if w := call(other, "PUT", updatePath, updateBody); w.Code != 404 {
		t.Fatal("foreign preference edit allowed")
	}
	for i := 0; i < 2; i++ {
		if w := call(owner, "PUT", updatePath, updateBody); w.Code != 200 || data(w)["preferencesRevision"] != float64(1) {
			t.Fatal("update/retry preferences", w.Body.String())
		}
	}
	if w := call(owner, "PUT", updatePath, `{"expectedRevision":0,"preferences":{"conversation":"quiet","air":"ac_on","music":"off"}}`); w.Code != 409 {
		t.Fatal("stale edit allowed")
	}
	if w := call(owner, "PUT", prefPath, `{"preferences":{}}`); w.Code != 409 {
		t.Fatal("consumed fare preferences mutable")
	}
	var notifications int
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM driver_notifications WHERE driver_id=$1::uuid AND data->>'tripId'=$2 AND title='Ride preferences updated'`, driver, trip).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("duplicate or missing preference notification", notifications, e)
	}
	var riderNotifications int
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM driver_notifications WHERE driver_id=$1::uuid AND data->>'tripId'=$2 AND title='Ride preferences updated'`, owner, trip).Scan(&riderNotifications); e != nil || riderNotifications != 0 {
		t.Fatal("booker incorrectly notified as Driver", e)
	}
	var guestName, guestPhone string
	if err := pool.QueryRow(ctx, `SELECT guest_name,guest_phone FROM trips WHERE id=$1::uuid`, trip).Scan(&guestName, &guestPhone); err != nil || guestName != "Amina" || guestPhone != "+2348012345678" {
		t.Fatal("guest lost at booking", err)
	}
	if w := call(owner, "PUT", guestPath, `{"name":"Other","phone":"+2348098765432"}`); w.Code != 409 {
		t.Fatal("guest changed after booking")
	}
	if w := call(driver, "GET", "/test/trips/"+trip+"/contact", ""); w.Code != 200 || data(w)["phone"] != "+2348012345678" {
		t.Fatal("wrong guest contact", w.Body.String())
	}
	if w := call(other, "GET", "/test/trips/"+trip+"/contact", ""); w.Code != 404 {
		t.Fatal("guest phone disclosed")
	}
	if w := call(driver, "GET", "/test/trips/"+trip+"/details", ""); w.Code != 200 || data(w)["guestPhone"] != "+2348012345678" {
		t.Fatal("active guest notes", w.Body.String())
	}
	if w := call(other, "GET", "/test/trips/"+trip+"/details", ""); w.Code != 404 {
		t.Fatal("foreign guest notes disclosed")
	}
	exec(`UPDATE trips SET status='completed' WHERE id=$1::uuid`, trip)
	if w := call(owner, "PUT", updatePath, `{"expectedRevision":1,"preferences":{}}`); w.Code != 409 {
		t.Fatal("terminal preference edit allowed")
	}
	if w := call(driver, "GET", "/test/trips/"+trip+"/contact", ""); w.Code != 404 {
		t.Fatal("terminal guest phone disclosed")
	}
	if w := call(driver, "GET", "/test/trips/"+trip+"/details", ""); w.Code != 200 || data(w)["guestPhone"] != nil {
		t.Fatal("terminal guest notes disclosed", w.Body.String())
	}
	exec(`UPDATE trips SET status='accepted' WHERE id=$1::uuid`, trip)
	var note string
	if pool.QueryRow(ctx, `SELECT pickup_note FROM trips WHERE id=$1::uuid`, trip).Scan(&note) != nil || note != "Meet at the blue gate" {
		t.Fatal("booking lost pickup note")
	}
	if w := call(owner, "PUT", notePath, `{"pickupNote":"too late"}`); w.Code != 409 {
		t.Fatal("consumed fare note changed")
	}
	lookup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		p := agent.Place{ID: req.PlaceID, Provider: "google", Name: req.PlaceID, Address: "Resolved address", Location: agent.Coordinate{Latitude: 6.95, Longitude: 3.95}}
		if req.PlaceID == "stop" {
			p.Location = agent.Coordinate{Latitude: 6.92, Longitude: 3.92}
		}
		_ = json.NewEncoder(w).Encode(agent.Response{SchemaVersion: 1, ConfirmationRequired: true, State: "resolved", Place: &p, Candidates: []agent.Place{p}})
	}))
	defer lookup.Close()
	t.Setenv("RIDE_AGENT_URL", lookup.URL)
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("x", 32))
	t.Setenv("RIDE_AGENT_AUDIENCE", "")
	previewPath := "/test/trips/" + trip + "/amendments/preview"
	body := `{"tripVersion":1,"destinationPlaceId":"new","stopPlaceIds":["stop"]}`
	if w := call(other, "POST", previewPath, body); w.Code != 404 {
		t.Fatalf("foreign preview: %s", w.Body.String())
	}
	if w := call(owner, "POST", previewPath, `{"tripVersion":1,"stopPlaceIds":["stop","stop"]}`); w.Code != 400 {
		t.Fatal("duplicate stops accepted")
	}
	w := call(owner, "POST", previewPath, body)
	if w.Code != 201 {
		t.Fatalf("preview: %s", w.Body.String())
	}
	quote := data(w)["quoteId"].(string)
	if data(w)["fareKobo"].(float64) != 3333 {
		t.Fatalf("wrong server fare: %s", w.Body.String())
	}
	confirmPath := "/rider/trips/" + trip + "/amendments/" + quote + "/confirm"
	if w := call(owner, "POST", confirmPath, `{"confirmed":false}`); w.Code != 400 {
		t.Fatal("implicit amendment accepted")
	}
	for i := 0; i < 2; i++ {
		if w := call(owner, "POST", confirmPath, `{"confirmed":true}`); w.Code != 200 {
			t.Fatalf("confirm/retry: %s", w.Body.String())
		}
	}
	var count int
	if pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE type='trip.event.amended' AND data->>'quoteId'=$1`, quote).Scan(&count) != nil || count != 2 {
		t.Fatalf("participant events=%d", count)
	}
	var currentFare string
	var rawStops []byte
	var version int64
	if err := pool.QueryRow(ctx, `SELECT ride_fare_id::text,stops,version FROM trips WHERE id=$1::uuid`, trip).Scan(&currentFare, &rawStops, &version); err != nil {
		t.Fatal(err)
	}
	if currentFare == fare || version != 2 {
		t.Fatal("fare history overwritten or version not advanced")
	}
	var oldPrice int64
	_ = pool.QueryRow(ctx, `SELECT ROUND(total_fare_minor)::bigint FROM ride_fares WHERE id=$1::uuid`, fare).Scan(&oldPrice)
	if oldPrice != 10000 {
		t.Fatal("original price changed")
	}
	if w := call(owner, "POST", previewPath, body); w.Code != 409 {
		t.Fatal("stale version accepted")
	}
	exec(`UPDATE trips SET status='started',started_at=NOW()-INTERVAL '10 minutes',version=version+1 WHERE id=$1::uuid`, trip)
	var stops []amendmentStop
	_ = json.Unmarshal(rawStops, &stops)
	stopPath := "/driver/trips/" + trip + "/stops/" + stops[0].ID + "/complete"
	if _, _, err := triprepo.NewPostgresRepository(pool).Complete(ctx, trip, driver); err == nil {
		t.Fatal("trip completed before required stop")
	}
	if w := call(driver, "POST", stopPath, `{"tripVersion":3}`); w.Code != 409 {
		t.Fatal("remote stop completion accepted")
	}
	exec(`UPDATE driver_live_locations SET location=ST_SetSRID(ST_MakePoint(3.92,6.92),4326)::geography,recorded_at=NOW() WHERE driver_id=$1::uuid`, driver)
	if w := call(driver, "POST", stopPath, `{"tripVersion":3}`); w.Code != 200 {
		t.Fatalf("stop completion: %s", w.Body.String())
	}
	// Observations are seeded as a continuous window; no synthetic clock sleeps.
	exec(`INSERT INTO trip_safety_monitor(trip_id,route_revision,anchor,stationary_since,last_at)SELECT id,route_revision,ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,NOW()-INTERVAL '5 minutes',NOW()-INTERVAL '30 seconds' FROM trips WHERE id=$1::uuid`, trip)
	observeAt := func(latitude, longitude float64) {
		t.Helper()
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = tripsafety.Observe(ctx, tx, trip, time.Now().UTC(), latitude, longitude); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	observe := func() { observeAt(6.9, 3.9) }
	observe()
	observe()
	var checkin string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM trip_safety_checkins WHERE trip_id=$1::uuid AND kind='ride_stopped'`, trip).Scan(&checkin); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_safety_checkins WHERE trip_id=$1::uuid AND kind='ride_stopped'`, trip).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate check-ins")
	}
	responsePath := "/rider/trips/" + trip + "/check-ins/" + checkin + "/respond"
	if w := call(other, "POST", responsePath, `{"response":"okay"}`); w.Code != 404 {
		t.Fatal("foreign safety response accepted")
	}
	for i := 0; i < 2; i++ {
		if w := call(owner, "POST", responsePath, `{"response":"need_help"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"emergencyDispatched":false`) {
			t.Fatal(w.Body.String())
		}
	}
	if w := call(owner, "POST", responsePath, `{"response":"okay"}`); w.Code != 409 {
		t.Fatal("check-in response overwritten")
	}
	// A long reconnect gap cannot count as continuous stopped time.
	exec(`UPDATE trip_safety_monitor SET last_at=NOW()-INTERVAL '3 minutes',stationary_since=NOW()-INTERVAL '20 minutes' WHERE trip_id=$1::uuid`, trip)
	observe()
	var recent bool
	if pool.QueryRow(ctx, `SELECT stationary_since>NOW()-INTERVAL '10 seconds' FROM trip_safety_monitor WHERE trip_id=$1::uuid`, trip).Scan(&recent) != nil || !recent {
		t.Fatal("location gap did not reset stopped window")
	}
	// Sustained off-route readings trigger once, with no assumption of danger.
	exec(`UPDATE trip_safety_monitor SET anchor=ST_SetSRID(ST_MakePoint(3.98,6.88),4326)::geography,stationary_since=NOW(),last_at=NOW()-INTERVAL '20 seconds',deviation_since=NOW()-INTERVAL '70 seconds',deviation_samples=2 WHERE trip_id=$1::uuid`, trip)
	observeAt(6.88, 3.98)
	observeAt(6.88, 3.98)
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_safety_checkins WHERE trip_id=$1::uuid AND kind='route_deviation'`, trip).Scan(&count)
	if count != 1 {
		t.Fatalf("route deviation deduplication: %d", count)
	}
	// A new planned stop suppresses a stationary prompt at its location.
	plannedRaw, _ := json.Marshal([]amendmentStop{{ID: uuid.NewString(), Place: agent.Place{ID: "planned", Provider: "google", Name: "Planned stop", Location: agent.Coordinate{Latitude: 6.9, Longitude: 3.9}}}})
	exec(`UPDATE trips SET route_revision=route_revision+1,stops=$2 WHERE id=$1::uuid`, trip, plannedRaw)
	exec(`UPDATE trip_safety_monitor SET route_revision=(SELECT route_revision FROM trips WHERE id=$1::uuid),anchor=ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,stationary_since=NOW()-INTERVAL '10 minutes',last_at=NOW()-INTERVAL '30 seconds',deviation_since=NULL,deviation_samples=0 WHERE trip_id=$1::uuid`, trip)
	observe()
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_safety_checkins WHERE trip_id=$1::uuid`, trip).Scan(&count)
	if count != 2 {
		t.Fatal("planned stop caused a false safety prompt")
	}
	exec(`UPDATE trips SET stops='[]'::jsonb WHERE id=$1::uuid`, trip)
	if w := call(owner, "GET", "/rider/trips/"+trip+"/check-ins", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"actionable": false`) && !strings.Contains(w.Body.String(), `"actionable":false`) {
		t.Fatal(w.Body.String())
	}
	// In-ride repricing adjusts only the remaining-route delta, not another base.
	_ = pool.QueryRow(ctx, `SELECT version FROM trips WHERE id=$1::uuid`, trip).Scan(&version)
	inRideBody := fmt.Sprintf(`{"tripVersion":%d,"destinationPlaceId":"new","stopPlaceIds":[]}`, version)
	w = call(owner, "POST", previewPath, inRideBody)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if data(w)["fareKobo"].(float64) != 3333 {
		t.Fatalf("base charged twice: %s", w.Body.String())
	}
	applyQuote := data(w)["quoteId"].(string)
	w = call(owner, "POST", previewPath, inRideBody)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	competing := data(w)["quoteId"].(string)
	if w := call(owner, "POST", "/rider/trips/"+trip+"/amendments/"+applyQuote+"/confirm", `{"confirmed":true}`); w.Code != 200 {
		t.Fatalf("in-ride amendment: %s", w.Body.String())
	}
	if w := call(owner, "POST", "/rider/trips/"+trip+"/amendments/"+competing+"/confirm", `{"confirmed":true}`); w.Code != 409 {
		t.Fatal("competing quote overwrote newer route")
	}
	// A new quote binds to the current started state and can be invalidated.
	_ = pool.QueryRow(ctx, `SELECT version FROM trips WHERE id=$1::uuid`, trip).Scan(&version)
	w = call(owner, "POST", previewPath, fmt.Sprintf(`{"tripVersion":%d,"destinationPlaceId":"new","stopPlaceIds":[]}`, version))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	quote = data(w)["quoteId"].(string)
	exec(`UPDATE trip_amendment_quotes SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, quote)
	if w := call(owner, "POST", "/rider/trips/"+trip+"/amendments/"+quote+"/confirm", `{"confirmed":true}`); w.Code != 409 {
		t.Fatal("expired quote accepted")
	}
}
