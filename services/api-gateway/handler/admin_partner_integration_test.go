package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestPartnerFiltersContextAndVerificationTimeline(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driverID, otherID, riderID, fareID, tripID, caseID, staffID, auditID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{driverID, "driver"}, {otherID, "driver"}, {riderID, "rider"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, item.id, "partner:"+item.id, "partner-human:"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ id, name string }{{driverID, "Amina Partner"}, {otherID, "Bello Partner"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name,admin_status) VALUES($1::UUID,$2,'pending')`, item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested,online_market_code) VALUES($1::UUID,'Amina','TEST-PLATE','sedan','available',TRUE,'LAGOS')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested,online_market_code) VALUES($1::UUID,'Bello','OTHER-PLATE','sedan','offline',TRUE,'ABUJA')`, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_live_locations(driver_id,location,recorded_at) VALUES($1::UUID,ST_SetSRID(ST_Point(3.4,6.5),4326)::geography,NOW())`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_socket_sessions(driver_id,session_id,expires_at) VALUES($1::UUID,$2::UUID,NOW()+INTERVAL '1 hour')`, driverID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,5500)`, driverID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		if _, err := pool.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at) VALUES($1::UUID,$2::UUID,$3,'approved',$4,NOW()+INTERVAL '10 days')`, uuid.NewString(), driverID, kind, "private-partner-key-"+kind+driverID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',5000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'assigned')`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,subject) VALUES($1::UUID,$2::UUID,'rider','safety',$3::UUID,'Private incident text')`, caseID, riderID, tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','compliance')`, staffID, "partner-staff-"+staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_audit(id,admin_id,action,target_driver_id,target_type,target_id,before_state,after_state)
		VALUES($1::UUID,$2::UUID,'driver.approval',$3::UUID,'driver',$3::TEXT,'{"status":"pending","secret":"PRIVATE_VALUE"}'::JSONB,'{"status":"approved"}'::JSONB)`, auditID, staffID, driverID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			return
		}
		defer tx.Rollback(cleanupCtx)
		if _, err := tx.Exec(cleanupCtx, `SET LOCAL session_replication_role = replica`); err != nil {
			return
		}
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM admin_audit WHERE id=$1::UUID`, auditID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM safety_incidents WHERE case_id=$1::UUID`, caseID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM support_cases WHERE id=$1::UUID`, caseID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM trip_status_events WHERE trip_id=$1::UUID`, tripID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM driver_documents WHERE driver_id=$1::UUID`, driverID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM driver_socket_sessions WHERE driver_id=$1::UUID`, driverID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM driver_live_locations WHERE driver_id=$1::UUID`, driverID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM driver_operating_accounts WHERE driver_id=$1::UUID`, driverID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM drivers WHERE id IN ($1::UUID,$2::UUID)`, driverID, otherID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM driver_profiles WHERE driver_id IN ($1::UUID,$2::UUID)`, driverID, otherID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID,$3::UUID)`, driverID, otherID, riderID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id=$1::UUID`, staffID)
		_ = tx.Commit(cleanupCtx)
	})
	gin.SetMode(gin.TestMode)
	api := &adminAPI{pool: pool}
	call := func(path string, handler gin.HandlerFunc, params gin.Params, role string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		gc, _ := gin.CreateTestContext(rec)
		gc.Request = httptest.NewRequest(http.MethodGet, path, nil)
		gc.Params = params
		gc.Set("admin", adminIdentity{Role: role})
		handler(gc)
		return rec
	}
	filters := url.Values{"status": {"pending"}, "q": {"Ami"}, "online": {"current"}, "zone": {"LAGOS"}, "balance": {"positive"}, "expiry": {"within_30_days"}}
	list := call("/admin/drivers?"+filters.Encode(), api.listDrivers, nil, "compliance")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), driverID) || strings.Contains(list.Body.String(), otherID) || strings.Contains(list.Body.String(), "balanceKobo") {
		t.Fatalf("filtered Compliance list: %d %s", list.Code, list.Body.String())
	}
	filters.Set("q", driverID)
	list = call("/admin/drivers?"+filters.Encode(), api.listDrivers, nil, "super_admin")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"balanceKobo":5500`) {
		t.Fatalf("exact Driver ID/Finance list: %d %s", list.Code, list.Body.String())
	}
	missing := url.Values{"status": {"pending"}, "q": {otherID}, "online": {"not_current"}, "balance": {"missing"}, "expiry": {"missing"}}
	list = call("/admin/drivers?"+missing.Encode(), api.listDrivers, nil, "compliance")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), otherID) || strings.Contains(list.Body.String(), driverID) {
		t.Fatalf("missing balance/expiry filters: %d %s", list.Code, list.Body.String())
	}
	params := gin.Params{{Key: "driverID", Value: driverID}}
	detail := call("/admin/drivers/"+driverID, api.driverReview, params, "compliance")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), caseID) || !strings.Contains(detail.Body.String(), tripID) || strings.Contains(detail.Body.String(), "Private incident text") || strings.Contains(detail.Body.String(), `"balanceKobo"`) {
		t.Fatalf("safe Partner context: %d %s", detail.Code, detail.Body.String())
	}
	timeline := call("/admin/drivers/"+driverID+"/verification-timeline?limit=2", api.partnerTimeline, params, "compliance")
	if timeline.Code != http.StatusOK || timeline.Header().Get("X-History-Coverage") != "partial-legacy" || !strings.Contains(timeline.Body.String(), "document.submitted") || strings.Contains(timeline.Body.String(), "private-partner-key") {
		t.Fatalf("safe Verification timeline: %d %s", timeline.Code, timeline.Body.String())
	}
	var page struct {
		Meta struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(timeline.Body.Bytes(), &page); err != nil || page.Meta.NextCursor == nil {
		t.Fatalf("timeline pagination: %s err=%v", timeline.Body.String(), err)
	}
	next := call("/admin/drivers/"+driverID+"/verification-timeline?limit=2&cursor="+url.QueryEscape(*page.Meta.NextCursor), api.partnerTimeline, params, "compliance")
	if next.Code != http.StatusOK || next.Body.String() == timeline.Body.String() {
		t.Fatalf("timeline second page: %d %s", next.Code, next.Body.String())
	}
	fullTimeline := call("/admin/drivers/"+driverID+"/verification-timeline?limit=20", api.partnerTimeline, params, "compliance")
	if fullTimeline.Code != http.StatusOK || !strings.Contains(fullTimeline.Body.String(), "driver.approval") || strings.Contains(fullTimeline.Body.String(), "PRIVATE_VALUE") {
		t.Fatalf("audit timeline redaction: %d %s", fullTimeline.Code, fullTimeline.Body.String())
	}
}
