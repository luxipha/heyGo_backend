package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

func TestRiderHomeOwnershipRecoveryAndAvailability(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Roll back all fixtures, including immutable policies and append-only records.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	rider, other, driver, creator, approver := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{rider, other, driver} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,phone_verified_at) VALUES($1::UUID,$1,$1,ARRAY['rider','driver'],NOW())`, id)
	}
	for _, id := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$1,'test')`, id)
	}
	market := "J03_" + uuid.NewString()
	exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,'market',$2,$2,1,ST_GeomFromText('MULTIPOLYGON(((3.8 6.8,4.0 6.8,4.0 7.0,3.8 7.0,3.8 6.8)))',4326),'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, uuid.NewString(), market, creator, approver)
	pricing := uuid.NewString()
	exec(`INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, pricing, market, creator, approver)
	exec(`INSERT INTO pricing_package_rates VALUES($1::UUID,'sedan',10000,100,100)`, pricing)
	exec(`INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,1,1,FALSE,0,'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, uuid.NewString(), market, creator, approver)
	exec(`INSERT INTO driver_profiles(driver_id,manual_verification_status,admin_status,admin_reviewed_at) VALUES($1::UUID,'approved','approved',NOW()+INTERVAL '1 second')`, driver)
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at) VALUES($1::UUID,$2::UUID,$3,'approved','test',NOW()+INTERVAL '1 day')`, uuid.NewString(), driver, kind)
	}
	exec(`INSERT INTO driver_inspections(id,driver_id,status) VALUES($1::UUID,$2::UUID,'completed')`, uuid.NewString(), driver)
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status,available,online_requested,online_market_code,location) VALUES($1::UUID,'Verified Driver','TEST','sedan','available',TRUE,TRUE,$2,ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography)`, driver, market)
	exec(`INSERT INTO driver_live_locations(driver_id,location) SELECT id,location FROM drivers WHERE id=$1::UUID`, driver)
	exec(`INSERT INTO driver_socket_sessions VALUES($1::UUID,$2::UUID,NOW()+INTERVAL '1 hour')`, driver, uuid.NewString())
	exec(`INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,1)`, driver)
	api := &riderHomeAPI{pool: tx}
	call := func(path, user string, handler gin.HandlerFunc, tripID string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", path, nil)
		c.Params = gin.Params{{Key: "tripID", Value: tripID}}
		c.Set("authenticated-user", gatewayauth.User{ID: user, Roles: []string{"rider"}})
		handler(c)
		return rec
	}
	active := call("/rider/trips/active", rider, api.activeTrip, "")
	if active.Code != 200 || !strings.Contains(active.Body.String(), `"trip":null`) {
		t.Fatalf("no active trip: %s", active.Body.String())
	}
	fare, trip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}',ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,ST_SetSRID(ST_MakePoint(3.91,6.91),4326)::geography)`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'assigned')`, trip, rider, fare, driver)
	for _, status := range []string{"pending", "assigned", "accepted", "arrived", "started"} {
		exec(`UPDATE trips SET status=$2 WHERE id=$1::UUID`, trip, status)
		response := call("/rider/trips/active", rider, api.activeTrip, "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), trip) {
			t.Fatalf("recover %s: %s", status, response.Body.String())
		}
		if (status == "pending" || status == "assigned") && !strings.Contains(response.Body.String(), `"driver":null`) {
			t.Fatal("offered driver was disclosed before acceptance")
		}
	}
	exec(`INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,responded_at,trip_payload) VALUES($1::UUID,$2::UUID,1,'accepted',NOW()+INTERVAL '1 hour',NOW(),'{}')`, trip, driver)
	for _, status := range []string{"accepted", "arrived", "started"} {
		exec(`UPDATE trips SET status=$2 WHERE id=$1::UUID`, trip, status)
		at := time.Now().UTC()
		if err := appendRiderLocation(ctx, tx, driver, at, 6.9, 3.9); err != nil {
			t.Fatalf("location %s: %v", status, err)
		}
		var payload []byte
		if err := tx.QueryRow(ctx, `SELECT data FROM user_events WHERE recipient_id=$1::UUID AND type=$2 ORDER BY id DESC LIMIT 1`, rider, contracts.DriverEventLocationUpdated).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), trip+":1") || !strings.Contains(string(payload), "capturedAt") {
			t.Fatalf("unscoped location: %s", payload)
		}
	}
	var before, after int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID`, rider).Scan(&before); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE driver_assignments SET status='cancelled' WHERE trip_id=$1::UUID`, trip)
	if err := appendRiderLocation(ctx, tx, driver, time.Now(), 6.9, 3.9); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID`, rider).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("former assignment forwarded location")
	}
	var appended int64
	hooked := &riderHandoffDB{riderHomeDB: tx, beforeSnapshot: func() {
		event, err := messaging.AppendEventTx(ctx, tx, rider, "handoff-test", "trip.event.started", json.RawMessage(`{"tripId":"`+trip+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Sscanf(event.ID, "evt_%d", &appended); err != nil {
			t.Fatal(err)
		}
	}}
	api.pool = hooked
	handoff := call("/rider/trips/active", rider, api.activeTrip, "")
	api.pool = tx
	var response struct {
		Data struct {
			AfterEventID string `json:"afterEventId"`
		}
	}
	if err := json.Unmarshal(handoff.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var baseline int64
	if _, err := fmt.Sscanf(response.Data.AfterEventID, "evt_%d", &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline >= appended {
		t.Fatal("event between snapshot and subscription was skipped")
	}
	if rec := call("/rider/trips/"+trip, other, api.tripDetail, trip); rec.Code != 404 {
		t.Fatal("another Rider can read trip")
	}
	if rec := call("/rider/trips/"+trip, rider, api.tripDetail, trip); rec.Code != 200 || strings.Contains(rec.Body.String(), "earningsKobo") {
		t.Fatal("owned snapshot unavailable or leaks earnings")
	}
	exec(`UPDATE trips SET status='completed' WHERE id=$1::UUID`, trip)
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,10000,'pending')`, trip, driver, rider)
	if rec := call("/rider/trips/active", rider, api.activeTrip, ""); !strings.Contains(rec.Body.String(), trip) {
		t.Fatal("pending settlement not recovered")
	}
	exec(`UPDATE trip_settlements SET status='confirmed',resolved_at=NOW() WHERE trip_id=$1::UUID`, trip)
	if rec := call("/rider/trips/active", rider, api.activeTrip, ""); !strings.Contains(rec.Body.String(), `"trip":null`) {
		t.Fatal("settled trip still active")
	}
	count := func(want int) {
		t.Helper()
		rec := call("/rider/availability?latitude=6.9&longitude=3.9&packageSlug=sedan", rider, api.availability, "")
		var body struct {
			Data struct {
				Count int `json:"count"`
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || body.Data.Count != want {
			t.Fatalf("availability want=%d status=%d body=%s", want, rec.Code, rec.Body.String())
		}
	}
	exec(`INSERT INTO driver_vehicles(driver_id,package_slug,review_status) VALUES($1::UUID,'sedan','approved')`, driver)
	count(1)
	for _, change := range []struct{ name, exclude, restore string }{
		{"balance", `UPDATE driver_operating_accounts SET balance_kobo=0 WHERE driver_id=$1::UUID`, `UPDATE driver_operating_accounts SET balance_kobo=1 WHERE driver_id=$1::UUID`},
		{"presence", `UPDATE driver_socket_sessions SET expires_at=NOW()-INTERVAL '1 minute' WHERE driver_id=$1::UUID`, `UPDATE driver_socket_sessions SET expires_at=NOW()+INTERVAL '1 hour' WHERE driver_id=$1::UUID`},
		{"freshness", `UPDATE driver_live_locations SET recorded_at=NOW()-INTERVAL '31 minutes' WHERE driver_id=$1::UUID`, `UPDATE driver_live_locations SET recorded_at=NOW() WHERE driver_id=$1::UUID`},
		{"package", `UPDATE driver_vehicles SET package_slug='suv' WHERE driver_id=$1::UUID`, `UPDATE driver_vehicles SET package_slug='sedan' WHERE driver_id=$1::UUID`},
		{"eligibility", `UPDATE users SET active=FALSE WHERE id=$1::UUID`, `UPDATE users SET active=TRUE WHERE id=$1::UUID`},
		{"available", `UPDATE drivers SET available=FALSE WHERE id=$1::UUID`, `UPDATE drivers SET available=TRUE WHERE id=$1::UUID`},
		{"online", `UPDATE drivers SET online_requested=FALSE WHERE id=$1::UUID`, `UPDATE drivers SET online_requested=TRUE WHERE id=$1::UUID`},
		{"market", `UPDATE drivers SET online_market_code='OTHER' WHERE id=$1::UUID`, `UPDATE drivers SET online_market_code='` + market + `' WHERE id=$1::UUID`},
		{"radius", `UPDATE drivers SET location=ST_SetSRID(ST_MakePoint(3.8,6.8),4326)::geography WHERE id=$1::UUID`, `UPDATE drivers SET location=ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography WHERE id=$1::UUID`},
	} {
		t.Run(change.name, func(t *testing.T) { exec(change.exclude, driver); count(0); exec(change.restore, driver); count(1) })
	}
	for _, path := range []string{"/rider/availability?latitude=NaN&longitude=3.9&packageSlug=sedan", "/rider/availability?latitude=6.9&longitude=3.9&packageSlug=unknown"} {
		if rec := call(path, rider, api.availability, ""); rec.Code != 400 {
			t.Fatalf("invalid query status=%d", rec.Code)
		}
	}
	if rec := call("/rider/availability?latitude=0&longitude=0&packageSlug=sedan", rider, api.availability, ""); rec.Code != 409 {
		t.Fatal("outside market should be unavailable")
	}
}

type riderHandoffDB struct {
	riderHomeDB
	beforeSnapshot func()
}

func (d *riderHandoffDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "SELECT t.id::TEXT") && d.beforeSnapshot != nil {
		hook := d.beforeSnapshot
		d.beforeSnapshot = nil
		hook()
	}
	return d.riderHomeDB.QueryRow(ctx, sql, args...)
}
