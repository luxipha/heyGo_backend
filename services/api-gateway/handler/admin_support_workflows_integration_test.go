package handler

import (
	"context"
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

func TestStaffSupportFinanceHandoff(t *testing.T) {
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
	staffID, riderID := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','support')`, staffID, "staff-case-"+staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, riderID, "staff-case:"+riderID, "staff-case-human:"+riderID); err != nil {
		t.Fatal(err)
	}
	var caseID string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		tx, err := pool.Begin(cleanup)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer tx.Rollback(cleanup)
		for _, sql := range []string{
			`ALTER TABLE support_case_escalations DISABLE TRIGGER support_case_escalations_append_only`,
			`ALTER TABLE support_case_outcomes DISABLE TRIGGER support_case_outcomes_append_only`,
		} {
			if _, err := tx.Exec(cleanup, sql); err != nil {
				t.Errorf("cleanup trigger: %v", err)
				return
			}
		}
		steps := []struct {
			query string
			args  []any
		}{
			{`DELETE FROM admin_audit WHERE admin_id=$1::UUID`, []any{staffID}},
			{`DELETE FROM support_case_escalations WHERE case_id=$1::UUID`, []any{caseID}},
			{`DELETE FROM support_case_outcomes WHERE case_id=$1::UUID`, []any{caseID}},
			{`DELETE FROM support_cases WHERE id=$1::UUID`, []any{caseID}},
			{`DELETE FROM users WHERE id=$1::UUID`, []any{riderID}},
			{`DELETE FROM admin_users WHERE id=$1::UUID`, []any{staffID}},
		}
		for _, step := range steps {
			if _, err := tx.Exec(cleanup, step.query, step.args...); err != nil {
				t.Errorf("cleanup %s: %v", step.query, err)
				return
			}
		}
		for _, sql := range []string{
			`ALTER TABLE support_case_escalations ENABLE TRIGGER support_case_escalations_append_only`,
			`ALTER TABLE support_case_outcomes ENABLE TRIGGER support_case_outcomes_append_only`,
		} {
			if _, err := tx.Exec(cleanup, sql); err != nil {
				t.Errorf("cleanup trigger: %v", err)
				return
			}
		}
		if err := tx.Commit(cleanup); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}()
	a := &supportAPI{pool: pool}
	call := func(method, path, body string, fn func(*gin.Context)) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Set("admin", adminIdentity{ID: staffID, Role: "support"})
		if caseID != "" {
			ctx.Params = gin.Params{{Key: "caseID", Value: caseID}}
		}
		fn(ctx)
		return rec
	}
	created := call(http.MethodPost, "/admin/support/cases", `{"ownerId":"`+riderID+`","audience":"rider","topicCode":"payment","subject":"Incorrect charge","reason":"Customer contacted staff"}`, a.adminCreateCase)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if err := pool.QueryRow(dbctx, `SELECT id::TEXT FROM support_cases WHERE opened_by=$1::UUID AND created_by_admin=$2::UUID`, riderID, staffID).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	if escalated := call(http.MethodPost, "/admin/support/cases/"+caseID+"/escalations", `{"reason":"Payment investigation needs Finance"}`, a.adminEscalateCase); escalated.Code != http.StatusCreated {
		t.Fatalf("escalate status=%d body=%s", escalated.Code, escalated.Body.String())
	}
	if outcome := call(http.MethodPost, "/admin/support/cases/"+caseID+"/outcome", `{"outcome":"referred_to_finance","reason":"Provider payment requires Finance review"}`, a.adminCaseOutcome); outcome.Code != http.StatusCreated {
		t.Fatalf("outcome status=%d body=%s", outcome.Code, outcome.Body.String())
	}
	if duplicate := call(http.MethodPost, "/admin/support/cases/"+caseID+"/outcome", `{"outcome":"resolved_no_money","reason":"Should be rejected"}`, a.adminCaseOutcome); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate outcome status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	finance := &adminAPI{pool: pool}
	if handoff := call(http.MethodGet, "/admin/finance/handoffs", "", finance.financeHandoffs); handoff.Code != http.StatusOK || !strings.Contains(handoff.Body.String(), caseID) {
		t.Fatalf("handoff status=%d body=%s", handoff.Code, handoff.Body.String())
	}
}
