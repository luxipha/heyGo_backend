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

func TestSuperAdminRoleChangeRevokesSessionsAndAudits(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	dbctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	superID, targetID := uuid.NewString(), uuid.NewString()
	token := "staff-management-token-" + uuid.NewString()
	for _, item := range []struct{ id, role string }{{superID, "super_admin"}, {targetID, "support"}} {
		if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused',$3)`, item.id, "staff-manage-"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2::UUID,NOW()+INTERVAL '1 hour')`, adminTokenHash(token), targetID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = pool.Exec(ctx, `DELETE FROM admin_audit WHERE admin_id=$1::UUID`, superID)
		_, _ = pool.Exec(ctx, `DELETE FROM admin_sessions WHERE admin_id=$1::UUID`, targetID)
		_, _ = pool.Exec(ctx, `DELETE FROM admin_users WHERE id IN ($1::UUID,$2::UUID)`, superID, targetID)
	}()
	api := &adminAPI{pool: pool}
	call := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Params = gin.Params{{Key: "staffID", Value: targetID}}
		ctx.Set("admin", adminIdentity{ID: superID, Username: "super", Role: "super_admin"})
		api.updateStaff(ctx)
		return rec
	}
	changed := call("/admin/staff/"+targetID, `{"roles":["operations","finance","safety"],"reason":"Launch coverage for small team"}`)
	if changed.Code != http.StatusOK {
		t.Fatalf("change status=%d body=%s", changed.Code, changed.Body.String())
	}
	var role string
	if err := pool.QueryRow(dbctx, `SELECT role FROM admin_users WHERE id=$1::UUID`, targetID).Scan(&role); err != nil || role != "operations" {
		t.Fatalf("role=%q err=%v", role, err)
	}
	var storedRoles []string
	if err := pool.QueryRow(dbctx, `SELECT ARRAY(SELECT role FROM admin_user_roles WHERE admin_id=$1::UUID ORDER BY role)`, targetID).Scan(&storedRoles); err != nil || len(storedRoles) != 3 {
		t.Fatalf("roles=%v err=%v", storedRoles, err)
	}
	var changedBody struct {
		Data staffAccount `json:"data"`
	}
	if err := json.Unmarshal(changed.Body.Bytes(), &changedBody); err != nil || len(changedBody.Data.Roles) != 3 {
		t.Fatalf("staff response=%s err=%v", changed.Body.String(), err)
	}
	var revoked bool
	if err := pool.QueryRow(dbctx, `SELECT revoked_at IS NOT NULL FROM admin_sessions WHERE token_hash=$1`, adminTokenHash(token)).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("session revoked=%t err=%v", revoked, err)
	}
	var action string
	if err := pool.QueryRow(dbctx, `SELECT action FROM admin_audit WHERE admin_id=$1::UUID ORDER BY created_at DESC LIMIT 1`, superID).Scan(&action); err != nil || action != "staff.account.updated" {
		t.Fatalf("audit action=%q err=%v", action, err)
	}
	var targetType, auditTargetID, before, after, reason string
	if err := pool.QueryRow(dbctx, `SELECT target_type,target_id,before_state::TEXT,after_state::TEXT,reason FROM admin_audit WHERE admin_id=$1::UUID AND action='staff.account.updated' ORDER BY created_at DESC LIMIT 1`, superID).Scan(&targetType, &auditTargetID, &before, &after, &reason); err != nil || targetType != "staff" || auditTargetID != targetID || !strings.Contains(before, "support") || !strings.Contains(after, "finance") || reason != "Launch coverage for small team" {
		t.Fatalf("audit target=%s/%s before=%s after=%s reason=%s err=%v", targetType, auditTargetID, before, after, reason, err)
	}
	freshToken := "staff-management-new-token-" + uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2::UUID,NOW()+INTERVAL '1 hour')`, adminTokenHash(freshToken), targetID); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/admin", api.authenticate, api.authorize)
	group.GET("/auth/me", api.me)
	group.GET("/finance/operating-entries", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	group.GET("/driver-ratings", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	group.GET("/drivers", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: adminCookieName, Value: freshToken})
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/admin/auth/me"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"roles"`) || !strings.Contains(rec.Body.String(), permissionLedgerRead) {
		t.Fatalf("me status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/admin/finance/operating-entries", "/admin/driver-ratings"} {
		if rec := get(path); rec.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := get("/admin/drivers"); rec.Code != http.StatusForbidden {
		t.Fatalf("driver permission status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The sole Super Admin cannot demote itself through this route.
	self := httptest.NewRecorder()
	selfCtx, _ := gin.CreateTestContext(self)
	selfCtx.Request = httptest.NewRequest(http.MethodPatch, "/admin/staff/"+superID, strings.NewReader(`{"role":"support"}`))
	selfCtx.Request.Header.Set("Content-Type", "application/json")
	selfCtx.Params = gin.Params{{Key: "staffID", Value: superID}}
	selfCtx.Set("admin", adminIdentity{ID: superID, Role: "super_admin"})
	api.updateStaff(selfCtx)
	if self.Code != http.StatusConflict {
		t.Fatalf("self change status=%d", self.Code)
	}
}
