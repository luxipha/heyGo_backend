package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/referrals"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReferralRegistrationAndDisabledLink(t *testing.T) {
	url := os.Getenv("REFERRAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("Disposable referral database required")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	staff, driver, fresh, existing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e = pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver']),($2::uuid,'{}'),($3::uuid,ARRAY['rider'])`, driver, fresh, existing)
	exec(`UPDATE users SET referral_signup_open=FALSE WHERE id=$1::uuid`, existing)
	code, e := referrals.OwnCode(ctx, pool, driver, "driver")
	if e != nil {
		t.Fatal(e)
	}
	settings, e := referrals.Read(ctx, pool)
	if e != nil {
		t.Fatal(e)
	}
	settings.Enabled = true
	tx, _ := pool.Begin(ctx)
	settings, e = referrals.Save(ctx, tx, settings, staff)
	if e != nil {
		t.Fatal(e)
	}
	tx.Commit(ctx)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerReferralPublicRoutes(r, pool)
	userID := fresh
	store := gatewayauth.NewPostgresUserStore(pool)
	group := r.Group("/", func(c *gin.Context) {
		u, e := store.ByID(ctx, userID)
		if e != nil {
			t.Fatal(e)
		}
		c.Set("authenticated-user", u)
	})
	registerReferralRoutes(group, pool)
	group.POST("/auth/register", func(c *gin.Context) { registerRoleHandler(c, store) })
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := call("GET", "/r/"+code, ""); w.Code != 200 || !strings.Contains(w.Body.String(), code) {
		t.Fatalf("invite page: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/rider/referrals/check?code="+code, ""); w.Code != 200 {
		t.Fatalf("new user lookup: %d %s", w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(map[string]string{"role": "rider", "referralCode": code})
	if w := call("POST", "/auth/register", string(raw)); w.Code != 200 {
		t.Fatalf("signup: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/rider/referrals/referrer", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canEnterCode":false`) || !strings.Contains(w.Body.String(), code) {
		t.Fatalf("referrer not locked: %d %s", w.Code, w.Body.String())
	}
	// Replaying exactly the same enrollment succeeds without a second attribution.
	if w := call("POST", "/auth/register", string(raw)); w.Code != 200 {
		t.Fatalf("registration replay: %d %s", w.Code, w.Body.String())
	}
	userID = existing
	if w := call("POST", "/auth/register", string(raw)); w.Code != 409 {
		t.Fatalf("existing user claim: %d %s", w.Code, w.Body.String())
	}
	// Pause affects links immediately without removing the existing attribution.
	settings.Enabled = false
	settings.DisabledNotice = "Campaign paused by Admin"
	tx, _ = pool.Begin(ctx)
	if _, e = referrals.Save(ctx, tx, settings, staff); e != nil {
		t.Fatal(e)
	}
	tx.Commit(ctx)
	if w := call("GET", "/r/"+code, ""); w.Code != 409 || !strings.Contains(w.Body.String(), settings.DisabledNotice) || strings.Contains(w.Body.String(), `id="copy"`) {
		t.Fatalf("disabled link: %d %s", w.Code, w.Body.String())
	}
	if p := adminRoutePermission("PUT", "/admin/referrals/settings"); p != permissionSettings {
		t.Fatal("settings permission", p)
	}
	if p := adminRoutePermission("POST", "/admin/referrals/rewards/approve"); p != permissionFinanceWrite {
		t.Fatal("approval permission", p)
	}
}
