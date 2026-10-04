package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestLivePresenceThresholds(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		age               time.Duration
		socket, requested bool
		want              string
	}{{29 * time.Second, true, true, "current"}, {30 * time.Second, true, true, "stale"}, {59 * time.Second, true, true, "stale"}, {60 * time.Second, true, true, "disconnected"}, {time.Second, false, true, "disconnected"}, {time.Second, true, false, "offline"}}
	for _, tc := range cases {
		at := now.Add(-tc.age)
		if got := livePresence(now, tc.requested, tc.socket, &at); got != tc.want {
			t.Errorf("age=%s socket=%t requested=%t got=%s want=%s", tc.age, tc.socket, tc.requested, got, tc.want)
		}
	}
	if got := livePresence(now, true, true, nil); got != "disconnected" {
		t.Fatalf("missing location got=%s", got)
	}
}

func TestSafetyIntakeAndLiveSnapshot(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	dbctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	staffID, riderID, driverID, caseID, financeCaseID, entryID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	activeFareID, activeTripID, waitingFareID, waitingTripID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','safety')`, staffID, "safety-live-"+staffID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}} {
		if _, err := pool.Exec(dbctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "live-safe:"+u.id, "live-safe-human:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,available,online_requested) VALUES($1::UUID,'Test Driver','PRIVATE-PLATE','sedan','available',TRUE,TRUE)`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO driver_live_locations(driver_id,location,recorded_at) VALUES($1::UUID,ST_SetSRID(ST_MakePoint(3.4,6.5),4326)::geography,NOW())`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO driver_socket_sessions(driver_id,session_id,expires_at) VALUES($1::UUID,$2::UUID,NOW()+INTERVAL '30 seconds')`, driverID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,subject) VALUES($1::UUID,$2::UUID,'rider','safety','Unsafe driving')`, caseID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,subject) VALUES($1::UUID,$2::UUID,'rider','payment','Missing payment')`, financeCaseID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup) VALUES($1::UUID,$3::UUID,'sedan',5000,'{}'::JSONB,ST_SetSRID(ST_MakePoint(3.41,6.51),4326)::geography),($2::UUID,$3::UUID,'sedan',6000,'{}'::JSONB,ST_SetSRID(ST_MakePoint(3.42,6.52),4326)::geography)`, activeFareID, waitingFareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$3::UUID,$4::UUID,$5::UUID,'accepted'),($2::UUID,$3::UUID,$6::UUID,NULL,'pending')`, activeTripID, waitingTripID, riderID, activeFareID, driverID, waitingFareID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `UPDATE support_cases SET trip_id=$2::UUID WHERE id=$1::UUID`, caseID, activeTripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,7500)`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key) VALUES($1::UUID,$2::UUID,'topup',7500,7500,$3)`, entryID, driverID, "test-entry:"+entryID); err != nil {
		t.Fatal(err)
	}
	var incidentID string
	if err := pool.QueryRow(dbctx, `SELECT id::TEXT FROM safety_incidents WHERE case_id=$1::UUID`, caseID).Scan(&incidentID); err != nil {
		t.Fatalf("safety intake missing: %v", err)
	}
	defer func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer tx.Rollback(ctx)
		// The migration protects audit actions from normal deletes. Disable its
		// user trigger only inside this disposable fixture-cleanup transaction.
		steps := []struct {
			sql  string
			args []any
		}{
			{`ALTER TABLE safety_incident_actions DISABLE TRIGGER safety_incident_actions_append_only`, nil},
			{`ALTER TABLE driver_operating_entries DISABLE TRIGGER driver_operating_entries_append_only`, nil},
			{`DELETE FROM admin_audit WHERE admin_id=$1::UUID`, []any{staffID}},
			{`DELETE FROM safety_incident_actions WHERE incident_id=$1::UUID`, []any{incidentID}},
			{`DELETE FROM safety_incidents WHERE id=$1::UUID`, []any{incidentID}},
			{`DELETE FROM support_cases WHERE id IN ($1::UUID,$2::UUID)`, []any{caseID, financeCaseID}},
			{`DELETE FROM trips WHERE id IN ($1::UUID,$2::UUID)`, []any{activeTripID, waitingTripID}},
			{`DELETE FROM ride_fares WHERE id IN ($1::UUID,$2::UUID)`, []any{activeFareID, waitingFareID}},
			{`DELETE FROM driver_operating_entries WHERE id=$1::UUID`, []any{entryID}},
			{`DELETE FROM driver_operating_accounts WHERE driver_id=$1::UUID`, []any{driverID}},
			{`DELETE FROM driver_socket_sessions WHERE driver_id=$1::UUID`, []any{driverID}},
			{`DELETE FROM driver_live_locations WHERE driver_id=$1::UUID`, []any{driverID}},
			{`DELETE FROM drivers WHERE id=$1::UUID`, []any{driverID}},
			{`DELETE FROM users WHERE id IN ($1::UUID,$2::UUID)`, []any{riderID, driverID}},
			{`DELETE FROM admin_users WHERE id=$1::UUID`, []any{staffID}},
			{`ALTER TABLE safety_incident_actions ENABLE TRIGGER safety_incident_actions_append_only`, nil},
			{`ALTER TABLE driver_operating_entries ENABLE TRIGGER driver_operating_entries_append_only`, nil},
		}
		for _, step := range steps {
			if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
				t.Errorf("cleanup %s: %v", step.sql, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}()
	api := &adminAPI{pool: pool}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/live/drivers", nil)
	api.liveDrivers(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), driverID) || strings.Contains(rec.Body.String(), "PRIVATE-PLATE") {
		t.Fatalf("live status=%d body=%s", rec.Code, rec.Body.String())
	}
	var live struct {
		Data []staffLiveDriver `json:"data"`
		Meta struct {
			AsOf                time.Time `json:"asOf"`
			RefreshAfterSeconds int       `json:"refreshAfterSeconds"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil || live.Meta.AsOf.IsZero() || live.Meta.RefreshAfterSeconds != 10 {
		t.Fatalf("live response=%s err=%v", rec.Body.String(), err)
	}
	foundCurrent := false
	for _, item := range live.Data {
		if item.DriverID == driverID && item.Presence == "current" {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Fatalf("new Driver missing from current presence: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/admin/safety/incidents/"+incidentID, strings.NewReader(`{"status":"acknowledged","severity":"high","reason":"Reviewed report"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "incidentID", Value: incidentID}}
	ctx.Set("admin", adminIdentity{ID: staffID, Role: "safety"})
	api.updateSafetyIncident(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"acknowledged"`) {
		t.Fatalf("safety update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var actions int
	if err := pool.QueryRow(dbctx, `SELECT COUNT(*) FROM safety_incident_actions WHERE incident_id=$1::UUID`, incidentID).Scan(&actions); err != nil || actions != 1 {
		t.Fatalf("actions=%d err=%v", actions, err)
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/safety/incidents/"+incidentID+"/actions", nil)
	ctx.Params = gin.Params{{Key: "incidentID", Value: incidentID}}
	api.safetyIncidentActions(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Reviewed report") {
		t.Fatalf("safety timeline status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	ctx.Set("admin", adminIdentity{ID: staffID, Role: "safety"})
	api.overview(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"current":1`) {
		t.Fatalf("overview status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), incidentID) || !strings.Contains(rec.Body.String(), `"safetyIncidents":1`) {
		t.Fatalf("Safety attention does not reconcile: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/safety/incidents?status=attention", nil)
	api.listSafetyIncidents(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), incidentID) {
		t.Fatalf("Safety attention queue status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/support/cases?status=attention", nil)
	ctx.Set("admin", adminIdentity{ID: staffID, Role: "support"})
	(&supportAPI{pool: pool}).adminCases(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), caseID) || !strings.Contains(rec.Body.String(), financeCaseID) {
		t.Fatalf("Support attention queue status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/live/markers", nil)
	ctx.Set("admin", adminIdentity{ID: staffID, Role: "safety"})
	api.liveMarkers(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), incidentID) || strings.Contains(rec.Body.String(), waitingTripID) {
		t.Fatalf("Safety markers status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/live/markers", nil)
	ctx.Set("admin", adminIdentity{ID: staffID, Role: "operations"})
	api.liveMarkers(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), waitingTripID) || strings.Contains(rec.Body.String(), incidentID) {
		t.Fatalf("Operations markers status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/finance/operating-entries?driverId="+driverID, nil)
	api.staffOperatingEntries(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), entryID) || strings.Contains(rec.Body.String(), "source_key") {
		t.Fatalf("finance ledger status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/finance/dispute-cases", nil)
	api.staffFinanceDisputes(ctx)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), financeCaseID) || strings.Contains(rec.Body.String(), "Missing payment") {
		t.Fatalf("finance case status=%d body=%s", rec.Code, rec.Body.String())
	}
}
