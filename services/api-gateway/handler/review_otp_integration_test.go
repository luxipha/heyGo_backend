package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReviewOTPRealSessionScopeAndRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
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
	staff := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, staff, "review-"+staff)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	admin := &adminAPI{pool: pool, settingsKey: key}
	users := gatewayauth.NewPostgresUserStore(pool)
	auth := &phoneAuthAPI{pool: pool, users: users, pepper: key, deliveryKey: key}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}); c.Next() })
	router.PUT("/settings", admin.putReviewOTP)
	router.GET("/channels", auth.authChannels)
	router.POST("/request", auth.requestEmailOTP)
	router.POST("/verify", auth.verifyEmailOTP)
	router.POST("/refresh", auth.refresh)
	router.GET("/protected", gatewayauth.NewSessionMiddleware(pool, users).Authenticate, func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	req := func(method, path, body, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(w, r)
		return w
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var e struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &e) != nil {
			t.Fatal("Invalid JSON")
		}
		return e.Data
	}
	email := "review-" + uuid.NewString() + "@example.com"
	expires := time.Now().UTC().Add(2 * time.Minute)
	settings := func(enabled bool, version int) string {
		b, _ := json.Marshal(gin.H{"enabled": enabled, "email": email, "code": "369258", "expiresAt": expires, "version": version, "reason": "App store testing"})
		return string(b)
	}
	saved := req("PUT", "/settings", settings(true, 0), "")
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	if strings.Contains(saved.Body.String(), "369258") {
		t.Fatal("Code leaked")
	}
	s, err := readReviewOTP(ctx, pool)
	if err != nil || string(s.Ciphertext) == "369258" {
		t.Fatal("Code not encrypted")
	}
	available := req("GET", "/channels", "", "")
	if available.Code != 200 || !data(available)["email"].(map[string]any)["enabled"].(bool) || strings.Contains(available.Body.String(), email) {
		t.Fatal("Channel availability/privacy failed")
	}
	if req("POST", "/request", `{"email":"another@example.com"}`, "").Code != 503 {
		t.Fatal("Review code exposed to another account")
	}
	request := req("POST", "/request", fmt.Sprintf(`{"email":%q}`, email), "")
	if request.Code != 202 || strings.Contains(request.Body.String(), "369258") {
		t.Fatal("Challenge failed/leaked")
	}
	challenge := data(request)["challengeId"].(string)
	verify := func(id, code string) string {
		return fmt.Sprintf(`{"email":%q,"challengeId":%q,"code":%q}`, email, id, code)
	}
	if req("POST", "/verify", verify(challenge, "000000"), "").Code != 401 {
		t.Fatal("Wrong code accepted")
	}
	verified := req("POST", "/verify", verify(challenge, "369258"), "")
	if verified.Code != 200 {
		t.Fatalf("verify: %d %s", verified.Code, verified.Body.String())
	}
	tokens := data(verified)
	access := tokens["accessToken"].(string)
	refresh := tokens["refreshToken"].(string)
	if req("GET", "/protected", "", access).Code != 200 {
		t.Fatal("Not a real authorized session")
	}
	if req("POST", "/verify", verify(challenge, "369258"), "").Code != 401 {
		t.Fatal("Challenge replay accepted")
	}
	rotated := req("POST", "/refresh", fmt.Sprintf(`{"refreshToken":%q}`, refresh), "")
	if rotated.Code != 200 {
		t.Fatalf("refresh: %d %s", rotated.Code, rotated.Body.String())
	}
	tokens = data(rotated)
	access = tokens["accessToken"].(string)
	refresh = tokens["refreshToken"].(string)
	expiry, e := time.Parse(time.RFC3339Nano, tokens["refreshExpiresAt"].(string))
	if e != nil || expiry.After(expires) {
		t.Fatal("Refresh escaped expiry")
	}
	sender := &recordingEmailOTPSender{}
	auth.emailSender = sender
	normal := req("POST", "/request", `{"email":"normal@example.com"}`, "")
	sent, code := sender.sent()
	if normal.Code != 202 || sent != "normal@example.com" || len(code) != 6 || data(normal)["delivery"] != "provider" {
		t.Fatal("Normal delivery changed")
	}
	auth.emailSender = nil
	pending := req("POST", "/request", fmt.Sprintf(`{"email":%q}`, email), "")
	if pending.Code != 202 {
		t.Fatal("Pending challenge failed")
	}
	if req("PUT", "/settings", settings(false, 0), "").Code != 409 {
		t.Fatal("Stale settings accepted")
	}
	if req("PUT", "/settings", settings(false, 1), "").Code != 200 {
		t.Fatal("Disable failed")
	}
	if req("GET", "/protected", "", access).Code != 401 {
		t.Fatal("Access not revoked")
	}
	if req("POST", "/refresh", fmt.Sprintf(`{"refreshToken":%q}`, refresh), "").Code != 401 {
		t.Fatal("Refresh not revoked")
	}
	if req("POST", "/verify", verify(data(pending)["challengeId"].(string), "369258"), "").Code != 401 {
		t.Fatal("Disabled challenge accepted")
	}
	if req("PUT", "/settings", settings(true, 2), "").Code != 200 {
		t.Fatal("Re-enable failed")
	}
	attempt := req("POST", "/request", fmt.Sprintf(`{"email":%q}`, email), "")
	if attempt.Code != 202 {
		t.Fatal("Challenge failed")
	}
	id := data(attempt)["challengeId"].(string)
	for i := 0; i < 5; i++ {
		if req("POST", "/verify", verify(id, "000000"), "").Code != 401 {
			t.Fatal("Wrong attempt accepted")
		}
	}
	if req("POST", "/verify", verify(id, "369258"), "").Code != 401 {
		t.Fatal("Attempt limit bypassed")
	}
	_, err = pool.Exec(ctx, `UPDATE admin_review_otp SET expires_at=NOW()-INTERVAL '1 second' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	if req("POST", "/request", fmt.Sprintf(`{"email":%q}`, email), "").Code != 503 {
		t.Fatal("Expired review mode accepted")
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE action='review_otp.settings.updated'`).Scan(&audits); err != nil || audits < 3 {
		t.Fatal("Missing audit records")
	}
	if adminRoutePermission(http.MethodPut, "/admin/settings/review-otp") != permissionSettings {
		t.Fatal("Missing permission")
	}
}
