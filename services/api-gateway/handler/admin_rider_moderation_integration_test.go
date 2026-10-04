package handler

import (
	"context"
	"fmt"
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

func TestRiderModerationBlocksOnlyNewTripsAndScopesFlags(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	riderID, driverID, fareID, tripID, safetyCaseID, supportCaseID, safetyID, supportID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	riderSafetyCaseID := uuid.NewString()
	for _, u := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}} {
		if _, err := pool.Exec(dbctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "rider-mod:"+u.id, "rider-mod-human:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []struct{ id, role string }{{safetyID, "safety"}, {supportID, "support"}} {
		if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused',$3)`, a.id, "rider-mod-"+a.id, a.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',5000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, tripID, riderID, fareID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,subject) VALUES($1::UUID,$3::UUID,'driver','safety',$5::UUID,'Rider safety report'),($2::UUID,$4::UUID,'rider','account',NULL,'Rider support request')`, safetyCaseID, supportCaseID, driverID, riderID, tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,subject) VALUES($1::UUID,$2::UUID,'rider','safety',$3::UUID,'Rider opened complaint')`, riderSafetyCaseID, riderID, tripID); err != nil {
		t.Fatal(err)
	}
	var incidentID string
	if err := pool.QueryRow(dbctx, `SELECT id::TEXT FROM safety_incidents WHERE case_id=$1::UUID`, safetyCaseID).Scan(&incidentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return
		}
		defer tx.Rollback(ctx)
		// Disposable fixture cleanup includes append-only evidence rows.
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return
		}
		_, _ = tx.Exec(ctx, `DELETE FROM admin_audit WHERE admin_id IN ($1::UUID,$2::UUID)`, safetyID, supportID)
		_, _ = tx.Exec(ctx, `DELETE FROM rider_restriction_actions WHERE restriction_id IN (SELECT id FROM rider_restrictions WHERE rider_id=$1::UUID)`, riderID)
		_, _ = tx.Exec(ctx, `DELETE FROM rider_flag_actions WHERE flag_id IN (SELECT id FROM rider_flags WHERE rider_id=$1::UUID)`, riderID)
		_, _ = tx.Exec(ctx, `DELETE FROM rider_flags WHERE rider_id=$1::UUID`, riderID)
		_, _ = tx.Exec(ctx, `DELETE FROM rider_restrictions WHERE rider_id=$1::UUID`, riderID)
		_, _ = tx.Exec(ctx, `DELETE FROM safety_incidents WHERE case_id=$1::UUID`, safetyCaseID)
		_, _ = tx.Exec(ctx, `DELETE FROM safety_incidents WHERE case_id=$1::UUID`, riderSafetyCaseID)
		_, _ = tx.Exec(ctx, `DELETE FROM support_cases WHERE id IN ($1::UUID,$2::UUID,$3::UUID)`, safetyCaseID, supportCaseID, riderSafetyCaseID)
		_, _ = tx.Exec(ctx, `DELETE FROM trip_status_events WHERE trip_id=$1::UUID`, tripID)
		_, _ = tx.Exec(ctx, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = tx.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = tx.Exec(ctx, `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID)`, riderID, driverID)
		_, _ = tx.Exec(ctx, `DELETE FROM admin_users WHERE id IN ($1::UUID,$2::UUID)`, safetyID, supportID)
		_ = tx.Commit(ctx)
	})
	api := &adminAPI{pool: pool}
	gin.SetMode(gin.TestMode)
	call := func(method, path, body, actor, role string, handler func(*gin.Context), params gin.Params) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Params = params
		ctx.Set("admin", adminIdentity{ID: actor, Role: role})
		handler(ctx)
		return rec
	}
	params := gin.Params{{Key: "riderID", Value: riderID}}
	reviewAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	riderComplaint := call(http.MethodPost, "/admin/riders/"+riderID+"/restrictions", fmt.Sprintf(`{"caseId":%q,"reason":"Complaint is not Rider evidence","reviewAt":%q}`, riderSafetyCaseID, reviewAt), safetyID, "safety", api.restrictRider, params)
	if riderComplaint.Code != http.StatusBadRequest {
		t.Fatalf("Rider complaint accepted as restriction evidence: %d %s", riderComplaint.Code, riderComplaint.Body.String())
	}
	apply := call(http.MethodPost, "/admin/riders/"+riderID+"/restrictions", fmt.Sprintf(`{"caseId":%q,"reason":"Driver safety report reviewed","reviewAt":%q}`, safetyCaseID, reviewAt), safetyID, "safety", api.restrictRider, params)
	if apply.Code != http.StatusCreated {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	var restricted bool
	if err := pool.QueryRow(dbctx, `SELECT rider_trip_restricted FROM users WHERE id=$1::UUID`, riderID).Scan(&restricted); err != nil || !restricted {
		t.Fatalf("restricted=%t err=%v", restricted, err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, uuid.NewString(), riderID, fareID); err == nil || !strings.Contains(err.Error(), "rider_trip_restricted") {
		t.Fatalf("new trip should be blocked: %v", err)
	}
	var existingStatus string
	if err := pool.QueryRow(dbctx, `SELECT status FROM trips WHERE id=$1::UUID`, tripID).Scan(&existingStatus); err != nil || existingStatus != "pending" {
		t.Fatalf("existing trip status=%s err=%v", existingStatus, err)
	}
	var restrictionID string
	if err := pool.QueryRow(dbctx, `SELECT id::TEXT FROM rider_restrictions WHERE rider_id=$1::UUID AND status='active'`, riderID).Scan(&restrictionID); err != nil {
		t.Fatal(err)
	}
	supportFlag := call(http.MethodPost, "/admin/riders/"+riderID+"/flags", fmt.Sprintf(`{"kind":"support","caseId":%q,"reason":"Case needs account review","reviewAt":%q}`, supportCaseID, reviewAt), supportID, "support", api.createRiderFlag, params)
	if supportFlag.Code != http.StatusCreated {
		t.Fatalf("Support flag status=%d body=%s", supportFlag.Code, supportFlag.Body.String())
	}
	wrongOwner := call(http.MethodPost, "/admin/riders/"+riderID+"/flags", fmt.Sprintf(`{"kind":"safety","caseId":%q,"incidentId":%q,"reason":"Rider safety concern reviewed","reviewAt":%q}`, safetyCaseID, incidentID, reviewAt), supportID, "support", api.createRiderFlag, params)
	if wrongOwner.Code != http.StatusForbidden {
		t.Fatalf("Support created Safety flag: %d %s", wrongOwner.Code, wrongOwner.Body.String())
	}
	safetyFlag := call(http.MethodPost, "/admin/riders/"+riderID+"/flags", fmt.Sprintf(`{"kind":"safety","caseId":%q,"incidentId":%q,"reason":"Rider safety concern reviewed","reviewAt":%q}`, safetyCaseID, incidentID, reviewAt), safetyID, "safety", api.createRiderFlag, params)
	if safetyFlag.Code != http.StatusCreated {
		t.Fatalf("Safety flag status=%d body=%s", safetyFlag.Code, safetyFlag.Body.String())
	}
	var safetyFlagID string
	if err := pool.QueryRow(dbctx, `SELECT id::TEXT FROM rider_flags WHERE rider_id=$1::UUID AND kind='safety' AND status='open'`, riderID).Scan(&safetyFlagID); err != nil {
		t.Fatal(err)
	}
	flagParams := gin.Params{{Key: "riderID", Value: riderID}, {Key: "flagID", Value: safetyFlagID}}
	wrongResolver := call(http.MethodPost, "/admin/riders/"+riderID+"/flags/"+safetyFlagID+"/resolve", `{"reason":"Incident reviewed"}`, supportID, "support", api.resolveRiderFlag, flagParams)
	if wrongResolver.Code != http.StatusForbidden {
		t.Fatalf("Support resolved Safety flag: %d %s", wrongResolver.Code, wrongResolver.Body.String())
	}
	resolved := call(http.MethodPost, "/admin/riders/"+riderID+"/flags/"+safetyFlagID+"/resolve", `{"reason":"Incident reviewed"}`, safetyID, "safety", api.resolveRiderFlag, flagParams)
	if resolved.Code != http.StatusOK {
		t.Fatalf("Safety resolve status=%d body=%s", resolved.Code, resolved.Body.String())
	}
	if err := pool.QueryRow(dbctx, `SELECT rider_trip_restricted FROM users WHERE id=$1::UUID`, riderID).Scan(&restricted); err != nil || !restricted {
		t.Fatal("flag unexpectedly lifted restriction")
	}
	liftParams := gin.Params{{Key: "riderID", Value: riderID}, {Key: "restrictionID", Value: restrictionID}}
	lift := call(http.MethodPost, "/admin/riders/"+riderID+"/restrictions/"+restrictionID+"/lift", `{"reason":"Safety review complete"}`, safetyID, "safety", api.liftRiderRestriction, liftParams)
	if lift.Code != http.StatusOK {
		t.Fatalf("lift status=%d body=%s", lift.Code, lift.Body.String())
	}
	if err := pool.QueryRow(dbctx, `SELECT rider_trip_restricted FROM users WHERE id=$1::UUID`, riderID).Scan(&restricted); err != nil || restricted {
		t.Fatalf("restriction not lifted: %t %v", restricted, err)
	}
	var audits int
	if err := pool.QueryRow(dbctx, `SELECT COUNT(*) FROM admin_audit WHERE admin_id IN ($1::UUID,$2::UUID) AND action IN ('rider.restriction.applied','rider.restriction.lifted','rider.flag.created','rider.flag.resolved')`, safetyID, supportID).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}
