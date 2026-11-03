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
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestHeroesReviewOTPScopeAndRevocation(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable database required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	staff := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	a := registerHeroesRoutes(r, pool)
	a.pepper = key
	a.email.settingsKey = key
	sends := 0
	a.email.staffEmailSender = func(context.Context, string, string, string) error {
		sends++
		return fmt.Errorf("email service disabled")
	}
	admin := &adminAPI{pool: pool, settingsKey: key}
	r.PUT("/test/review", func(c *gin.Context) {
		c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"})
		admin.putReviewOTP(c)
	})
	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		q := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		var out map[string]any
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Body.String())
		}
		return w.Code, out
	}
	require := func(want, got int, out any) {
		t.Helper()
		if want != got {
			t.Fatalf("want %d got %d: %v", want, got, out)
		}
	}
	email := "hero-review-" + uuid.NewString() + "@example.test"
	expiry := time.Now().UTC().Add(15 * time.Minute)
	code, out := call("PUT", "/test/review", "", gin.H{"enabled": true, "email": email, "code": "369258", "expiresAt": expiry, "version": 0, "reason": "Heroes local testing"})
	require(200, code, out)
	code, out = call("POST", "/heroes/auth/otp/request", "", gin.H{"email": email})
	require(200, code, out)
	data := out["data"].(map[string]any)
	if data["delivery"] != "review" || sends != 0 {
		t.Fatal("Review tried email delivery")
	}
	challenge := data["challengeId"].(string)
	if strings.Contains(fmt.Sprint(out), "369258") {
		t.Fatal("Review code leaked")
	}
	code, out = call("POST", "/heroes/auth/otp/verify", "", gin.H{"challengeId": challenge, "code": "369258"})
	require(200, code, out)
	token := out["data"].(map[string]any)["token"].(string)
	if out["data"].(map[string]any)["expiresIn"].(float64) > 900 {
		t.Fatal("Session exceeded review expiry")
	}
	code, out = call("GET", "/heroes/me", token, nil)
	require(200, code, out)
	code, out = call("POST", "/heroes/auth/otp/request", "", gin.H{"email": "other-" + uuid.NewString() + "@example.test"})
	require(503, code, out)
	if sends != 1 {
		t.Fatal("Other email incorrectly used review bypass")
	}
	_, err = pool.Exec(ctx, `UPDATE heroes_email_otps SET requested_at=now()-interval '2 minutes' WHERE email=$1`, email)
	if err != nil {
		t.Fatal(err)
	}
	code, out = call("POST", "/heroes/auth/otp/request", "", gin.H{"email": email})
	require(200, code, out)
	challenge = out["data"].(map[string]any)["challengeId"].(string)
	code, out = call("PUT", "/test/review", "", gin.H{"enabled": false, "email": email, "expiresAt": expiry, "version": 1, "reason": "End Heroes testing"})
	require(200, code, out)
	code, out = call("GET", "/heroes/me", token, nil)
	require(401, code, out)
	code, out = call("POST", "/heroes/auth/otp/verify", "", gin.H{"challengeId": challenge, "code": "369258"})
	require(400, code, out)
}
