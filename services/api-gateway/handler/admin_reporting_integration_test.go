package handler

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func reportingPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
func TestAdminReportingAggregates(t *testing.T) {
	pool := reportingPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	user, admin := uuid.NewString(), uuid.NewString()
	market := "report-test-" + uuid.NewString()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['rider'])`, []any{user}},
		{`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'unused')`, []any{admin}},
	} {
		if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM support_cases`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"open", "closed"} {
		if _, err := tx.Exec(ctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,subject,status,assigned_admin_id) VALUES($1,$2::uuid,'rider','general-other','Reporting fixture',$3,$4::uuid)`, uuid.New(), user, status, admin); err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err := tx.QueryRow(ctx, supportSummarySQL).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var support struct {
		Total int `json:"total"`
		CSAT  any `json:"csat"`
	}
	if err = json.Unmarshal(raw, &support); err != nil || support.Total != before+2 || support.CSAT != nil {
		t.Fatalf("bad support summary: %s (%v)", raw, err)
	}
	var safetyBefore int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM safety_incidents`).Scan(&safetyBefore); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"open", "resolved"} {
		caseID := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,subject,assigned_admin_id) VALUES($1,$2::uuid,'rider','general-safety','Safety aggregate fixture',$3::uuid)`, caseID, user, admin); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE safety_incidents SET status=$2,severity='critical',assigned_admin_id=$3::uuid,resolved_at=CASE WHEN $2='resolved' THEN NOW() ELSE NULL END WHERE case_id=$1`, caseID, state, admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRow(ctx, safetySummarySQL).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var safety struct {
		Total        int `json:"total"`
		SLA          any `json:"sla"`
		Restrictions any `json:"restrictions"`
		Alerts       []struct {
			ID string `json:"id"`
		} `json:"alerts"`
	}
	if err = json.Unmarshal(raw, &safety); err != nil || safety.Total != safetyBefore+2 || safety.SLA != nil || safety.Restrictions != nil {
		t.Fatalf("bad safety aggregate %s (%v)", raw, err)
	}
	for _, alert := range safety.Alerts {
		var state string
		if err := tx.QueryRow(ctx, `SELECT status FROM safety_incidents WHERE id=$1::uuid`, alert.ID).Scan(&state); err != nil || state == "resolved" {
			t.Fatal("resolved or fictional incident in critical alerts")
		}
	}
	now := time.Now().UTC()
	for _, status := range []string{"completed", "cancelled"} {
		fare := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1,$2::uuid,'standard',12345,'{}')`, fare, user); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status,market_code,created_at) VALUES($1,$2::uuid,$3,$4,$5,$6)`, uuid.New(), user, fare, status, market, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRow(ctx, analyticsSummarySQL, now.AddDate(0, 0, -7), now, market).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Trips      int     `json:"totalTrips"`
		Fare       int     `json:"completedFareKobo"`
		Completion float64 `json:"completionRate"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Trips != 2 || result.Fare != 12345 || result.Completion != 50 {
		t.Fatalf("bad analytics: %s (%v)", raw, err)
	}
	if err := tx.QueryRow(ctx, analyticsSummarySQL, now.AddDate(0, 0, -7), now, "missing-"+market).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var empty struct {
		Trips int      `json:"totalTrips"`
		Rate  *float64 `json:"completionRate"`
	}
	if err = json.Unmarshal(raw, &empty); err != nil || empty.Trips != 0 || empty.Rate != nil {
		t.Fatalf("bad empty analytics: %s", raw)
	}
}
func TestCampaignDraftPersistenceConflictAndIsolation(t *testing.T) {
	pool := reportingPool(t)
	ctx := context.Background()
	actor, id := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::uuid,$1,'unused','super_admin')`, actor); err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Exec(ctx, `DELETE FROM admin_audit WHERE admin_id=$1::uuid`, actor)
		pool.Exec(ctx, `DELETE FROM admin_campaign_drafts WHERE created_by=$1::uuid`, actor)
		pool.Exec(ctx, `DELETE FROM admin_users WHERE id=$1::uuid`, actor)
	}()
	r := campaignTestRouter(&adminAPI{pool: pool}, actor)
	body := map[string]any{"id": id, "name": "Saved draft", "category": "Discount", "audience": "Riders under review", "body": "Proposed rules only", "budgetKobo": 12300, "reason": "Review before activation"}
	if w := campaignTestCall(r, "POST", "/admin/promotions/campaigns", body, true, "super_admin"); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	if w := campaignTestCall(r, "GET", "/admin/communications/campaigns/"+id, nil, false, "super_admin"); w.Code != 404 {
		t.Fatalf("kind leaked %d", w.Code)
	}
	body["version"] = 1
	body["name"] = "Revised draft"
	if w := campaignTestCall(r, "PATCH", "/admin/promotions/campaigns/"+id, body, true, "super_admin"); w.Code != 200 {
		t.Fatalf("update %d %s", w.Code, w.Body.String())
	}
	if w := campaignTestCall(r, "PATCH", "/admin/promotions/campaigns/"+id, body, true, "super_admin"); w.Code != 409 {
		t.Fatalf("lost update allowed %d", w.Code)
	}
	if w := campaignTestCall(r, "GET", "/admin/promotions/campaigns?limit=1", nil, false, "super_admin"); w.Code != 200 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	if w := campaignTestCall(r, "POST", "/admin/promotions/campaigns/"+id+"/activate", nil, true, "super_admin"); w.Code != 409 {
		t.Fatal("activation allowed")
	}
	var status string
	var version, audits int
	if err := pool.QueryRow(ctx, `SELECT status,version FROM admin_campaign_drafts WHERE id=$1::uuid`, id).Scan(&status, &version); err != nil || status != "draft" || version != 2 {
		t.Fatalf("bad persisted draft %s %d %v", status, version, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE admin_id=$1::uuid AND target_type='campaign' AND target_id=$2`, actor, id).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit %d %v", audits, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_campaign_drafts SET status='active' WHERE id=$1::uuid`, id); err == nil {
		t.Fatal("database allowed activation")
	}
}
