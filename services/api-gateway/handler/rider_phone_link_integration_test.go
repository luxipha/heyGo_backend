package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type riderPhoneTestSender struct {
	contactTestSender
	fail bool
}

func (s *riderPhoneTestSender) SendOTP(ctx context.Context, destination, code string) error {
	if s.fail {
		return errors.New("test delivery failure")
	}
	return s.contactTestSender.SendOTP(ctx, destination, code)
}
func TestRiderPhoneLinkAndBookingReadiness(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	owner, other := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		exec(`INSERT INTO users(id,email,email_verified,roles)VALUES($1::uuid,$2,TRUE,ARRAY['rider'])`, id, id+"@example.test")
	}
	sender := &riderPhoneTestSender{contactTestSender: contactTestSender{codes: map[string]string{}}}
	r := gin.New()
	g := r.Group("", func(c *gin.Context) {
		role := c.GetHeader("X-Role")
		if role == "" {
			role = "rider"
		}
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Owner"), Roles: []string{role}})
		c.Next()
	})
	registerRiderPhoneRoutes(g, pool, PhoneAuthConfig{OTPPepper: []byte(strings.Repeat("p", 32)), Sender: sender})
	count := 0
	call := func(who, method, path, body, role string) *httptest.ResponseRecorder {
		count++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Owner", who)
		req.Header.Set("X-Role", role)
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", count)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := call(owner, "GET", "/rider/booking-eligibility", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"phoneVerified":false`) || !strings.Contains(w.Body.String(), `"ready":false`) {
		t.Fatal("email-only readiness", w.Body.String())
	}
	if w := call(owner, "POST", "/rider/phone/request", `{"phone":"+2348012345678"}`, "driver"); w.Code != 403 {
		t.Fatal("role guard", w.Code)
	}
	if w := call(owner, "POST", "/rider/phone/request", `{"phone":"invalid"}`, ""); w.Code != 400 {
		t.Fatal("invalid number", w.Code)
	}
	phone := "+2348098765432"
	w := call(owner, "POST", "/rider/phone/request", `{"phone":"`+phone+`"}`, "")
	if w.Code != 202 {
		t.Fatal("initial link", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Challenge string `json:"challengeId"`
		}
	}
	if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil || out.Data.Challenge == "" {
		t.Fatal(e, w.Body.String())
	}
	code := sender.codes[phone]
	if code == "" || strings.Contains(w.Body.String(), code) {
		t.Fatal("missing or leaked code")
	}
	if w = call(owner, "POST", "/rider/phone/request", `{"phone":"`+phone+`"}`, ""); w.Code != 429 {
		t.Fatal("cooldown", w.Code)
	}
	confirm := func(who, value string) *httptest.ResponseRecorder {
		return call(who, "POST", "/rider/phone/confirm", `{"challengeId":"`+out.Data.Challenge+`","code":"`+value+`"}`, "")
	}
	if w = confirm(other, code); w.Code != 401 {
		t.Fatal("foreign challenge", w.Code)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if w = confirm(owner, wrong); w.Code != 401 {
		t.Fatal("wrong code", w.Code)
	}
	exec(`UPDATE rider_phone_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, out.Data.Challenge)
	if w = confirm(owner, code); w.Code != 401 {
		t.Fatal("expired", w.Code)
	}
	exec(`UPDATE rider_phone_challenges SET expires_at=NOW()+INTERVAL '5 minutes',attempts=5 WHERE id=$1::uuid`, out.Data.Challenge)
	if w = confirm(owner, code); w.Code != 401 {
		t.Fatal("attempt limit", w.Code)
	}
	exec(`UPDATE rider_phone_challenges SET attempts=0 WHERE id=$1::uuid`, out.Data.Challenge)
	exec(`UPDATE users SET phone_e164='+2348098765435' WHERE id=$1::uuid`, owner)
	if w = confirm(owner, code); w.Code != 409 {
		t.Fatal("stale account challenge", w.Code, w.Body.String())
	}
	exec(`UPDATE users SET phone_e164=NULL WHERE id=$1::uuid`, owner)
	if w = confirm(owner, code); w.Code != 200 {
		t.Fatal("correct code", w.Code, w.Body.String())
	}
	if w = confirm(owner, code); w.Code != 401 {
		t.Fatal("replay", w.Code)
	}
	if w = call(owner, "GET", "/rider/booking-eligibility", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) {
		t.Fatal("verified readiness", w.Body.String())
	}
	if w = call(other, "POST", "/rider/phone/request", `{"phone":"`+phone+`"}`, ""); w.Code != 409 {
		t.Fatal("collision", w.Code, w.Body.String())
	}
	if w = call(owner, "POST", "/rider/phone/request", `{"phone":"+2348098765433"}`, ""); w.Code != 409 {
		t.Fatal("verified number replacement bypass", w.Code)
	}
	sender.fail = true
	if w = call(other, "POST", "/rider/phone/request", `{"phone":"+2348098765436"}`, ""); w.Code != 502 {
		t.Fatal("delivery failure", w.Code, w.Body.String())
	}
	var verified bool
	if e = pool.QueryRow(ctx, `SELECT phone_verified_at IS NOT NULL FROM users WHERE id=$1::uuid`, other).Scan(&verified); e != nil || verified {
		t.Fatal("delivery failure verified account", verified, e)
	}
	var liveChallenges int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM rider_phone_challenges WHERE rider_id=$1::uuid AND consumed_at IS NULL`, other).Scan(&liveChallenges); e != nil || liveChallenges != 0 {
		t.Fatal("failed delivery left a usable code", liveChallenges, e)
	}

}
