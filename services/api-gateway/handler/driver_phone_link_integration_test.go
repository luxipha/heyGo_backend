package handler

import (
	"context"
	"encoding/json"
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

func TestDriverPhoneLinkOwnershipAndOnlineGate(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
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
	owner, other := uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{owner, other} {
		exec(`INSERT INTO users(id,email,email_verified,roles) VALUES($1::uuid,$2,TRUE,ARRAY['driver'])`, id, id+"@example.com")
		exec(`INSERT INTO driver_profiles(driver_id,manual_verification_status,admin_status) VALUES($1::uuid,'approved','approved')`, id)
	}
	sender := &contactTestSender{codes: map[string]string{}}
	r := gin.New()
	g := r.Group("", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Test-Owner"), Roles: []string{"driver"}})
	})
	registerDriverPhoneRoutes(g, pool, PhoneAuthConfig{OTPPepper: []byte(strings.Repeat("p", 32)), Sender: sender})
	api := &driverAPI{pool: pool}
	g.POST("/availability", api.availability)
	counter := 0
	call := func(path, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		counter++
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", counter)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Owner", id)
		r.ServeHTTP(w, req)
		return w
	}
	if w := call("/availability", owner, `{"available":true}`); w.Code != 403 || !strings.Contains(w.Body.String(), "phone_verification_required") {
		t.Fatalf("missing online gate: %d %s", w.Code, w.Body)
	}
	if w := call("/driver/phone/request", owner, `{"phone":"invalid"}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	phone := "+2348012345678"
	w := call("/driver/phone/request", owner, `{"phone":"`+phone+`"}`)
	if w.Code != 202 {
		t.Fatalf("request: %d %s", w.Code, w.Body)
	}
	var out struct {
		Data struct {
			Challenge string `json:"challengeId"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if strings.Contains(w.Body.String(), sender.codes[phone]) {
		t.Fatal("OTP leaked")
	}
	if w := call("/driver/phone/request", owner, `{"phone":"`+phone+`"}`); w.Code != 429 {
		t.Fatalf("cooldown: %d", w.Code)
	}
	confirm := func(id, code string) *httptest.ResponseRecorder {
		return call("/driver/phone/confirm", id, `{"challengeId":"`+out.Data.Challenge+`","code":"`+code+`"}`)
	}
	if w := confirm(other, sender.codes[phone]); w.Code != 401 {
		t.Fatalf("cross owner: %d", w.Code)
	}
	wrong := "000000"
	if sender.codes[phone] == wrong {
		wrong = "111111"
	}
	if w := confirm(owner, wrong); w.Code != 401 {
		t.Fatalf("wrong code: %d", w.Code)
	}
	if w := confirm(owner, sender.codes[phone]); w.Code != 200 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body)
	}
	if w := confirm(owner, sender.codes[phone]); w.Code != 401 {
		t.Fatalf("replay: %d", w.Code)
	}
	var actual, email, status string
	var verified bool
	if err = pool.QueryRow(ctx, `SELECT phone_e164,email,phone_verified_at IS NOT NULL FROM users WHERE id=$1::uuid`, owner).Scan(&actual, &email, &verified); err != nil {
		t.Fatal(err)
	}
	if actual != phone || !verified || email != owner+"@example.com" {
		t.Fatal("existing account not preserved")
	}
	pool.QueryRow(ctx, `SELECT manual_verification_status FROM driver_profiles WHERE driver_id=$1::uuid`, owner).Scan(&status)
	if status != "approved" {
		t.Fatal("identity approval reset")
	}
	if w := call("/availability", owner, `{"available":true}`); strings.Contains(w.Body.String(), "phone_verification_required") {
		t.Fatal("verified account still blocked by phone")
	}
	if w := call("/driver/phone/request", other, `{"phone":"`+phone+`"}`); w.Code != 409 {
		t.Fatalf("collision: %d %s", w.Code, w.Body)
	}
	if w := call("/driver/phone/request", owner, `{"phone":"+2348012345679"}`); w.Code != 409 {
		t.Fatalf("replacement safeguard: %d", w.Code)
	}
	// Both public OTP login channels must resolve to the existing linked account.
	for channel, destination := range map[string]string{"email": owner + "@example.com", "phone": phone} {
		challenge := uuid.NewString()
		pepper := []byte(strings.Repeat("p", 32))
		exec(`INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at) VALUES($1::uuid,$2,$3,$4,NOW()+INTERVAL '5 minutes')`, challenge, channel, destination, otpHash(pepper, challenge, channel+":"+destination, "123456"))
		recorder := httptest.NewRecorder()
		loginContext, _ := gin.CreateTestContext(recorder)
		loginContext.Request = httptest.NewRequest("POST", "/auth/otp/verify", nil)
		auth := &phoneAuthAPI{pool: pool, users: gatewayauth.NewPostgresUserStore(pool), pepper: pepper}
		auth.verifyChallenge(loginContext, channel, destination, challenge, "123456", "phone-link-test")
		if recorder.Code != 200 {
			t.Fatalf("%s login failed: %d %s", channel, recorder.Code, recorder.Body)
		}
		var session struct {
			Data struct {
				User struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"data"`
		}
		if err = json.Unmarshal(recorder.Body.Bytes(), &session); err != nil || session.Data.User.ID != owner {
			t.Fatalf("%s opened another account", channel)
		}
	}
	// Identity reads expose only the canonical number, including before an identity profile exists.
	identityContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	identityContext.Request = httptest.NewRequest("GET", "/", nil)
	identity, err := readIdentityProfile(identityContext, pool, owner)
	if err != nil || identity.Phone != phone || !identity.PhoneVerified {
		t.Fatalf("identity contact: %v %+v", err, identity)
	}
	var eventCount int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::uuid AND type='driver.phone.verified'`, owner).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("audit event count: %d", eventCount)
	}
	// A second actor cannot finish an expired or exhausted challenge.
	second := "+2348012345680"
	w = call("/driver/phone/request", other, `{"phone":"`+second+`"}`)
	if w.Code != 202 {
		t.Fatal(w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	exec(`UPDATE driver_phone_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, out.Data.Challenge)
	if w := confirm(other, sender.codes[second]); w.Code != 401 {
		t.Fatal("expired accepted")
	}
	exec(`UPDATE driver_phone_challenges SET expires_at=NOW()+INTERVAL '5 minutes',attempts=5 WHERE id=$1::uuid`, out.Data.Challenge)
	if w := confirm(other, sender.codes[second]); w.Code != 401 {
		t.Fatal("exhausted accepted")
	}
	// Staff assistance uses the same OTP proof, plus staff permissions, CSRF and audit.
	staff := uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$2,'test')`, staff, "phone-"+staff)
	adminAPI := &adminAPI{pool: pool, phoneLink: newDriverPhoneAPI(pool, PhoneAuthConfig{OTPPepper: []byte(strings.Repeat("p", 32)), Sender: sender})}
	ar := gin.New()
	ag := ar.Group("/admin", func(c *gin.Context) {
		c.Set("admin", adminIdentity{ID: staff, Role: c.GetHeader("X-Test-Role")})
		c.Set("admin-csrf", "expected")
	})
	registerAdminReviewRoutes(ag, adminAPI)
	staffConfirm := func(role, csrf string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/admin/drivers/"+other+"/phone/confirm", strings.NewReader(`{"challengeId":"`+out.Data.Challenge+`","code":"`+sender.codes[second]+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Role", role)
		req.Header.Set("X-CSRF-Token", csrf)
		ar.ServeHTTP(w, req)
		return w
	}
	if w := staffConfirm("compliance", ""); w.Code != 403 || !strings.Contains(w.Body.String(), "csrf_required") {
		t.Fatalf("missing CSRF gate: %d %s", w.Code, w.Body)
	}
	if w := staffConfirm("finance", "expected"); w.Code != 403 {
		t.Fatalf("missing staff permission: %d", w.Code)
	}
	exec(`UPDATE driver_phone_challenges SET expires_at=NOW()+INTERVAL '5 minutes',attempts=0 WHERE id=$1::uuid`, out.Data.Challenge)
	if w := staffConfirm("compliance", "expected"); w.Code != 200 {
		t.Fatalf("staff confirm: %d %s", w.Code, w.Body)
	}

}
