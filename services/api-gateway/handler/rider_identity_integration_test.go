package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type contactTestSender struct{ codes map[string]string }

func (s *contactTestSender) SendOTP(_ context.Context, destination, code string) error {
	s.codes[destination] = code
	return nil
}

func TestRiderIdentityOwnershipVerificationCollisionAndSessionRevocation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.NewString(), uuid.NewString()
	var phone string
	for _, id := range []string{owner, other} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,roles) VALUES($1::UUID,ARRAY['rider','driver'])`, id); err != nil {
			t.Fatal(err)
		}
	}
	// The uniqueness suffix is derived from a random UUID as decimal digits.
	var suffix uint64
	for _, b := range []byte(owner) {
		suffix = (suffix*31 + uint64(b)) % 1000000000
	}
	phone = fmtPhone(suffix)
	newPhone := fmtPhone((suffix + 1) % 1000000000)
	foreignPhone := fmtPhone((suffix + 2) % 1000000000)
	for id, p := range map[string]string{owner: phone, other: foreignPhone} {
		if _, err = pool.Exec(ctx, `UPDATE users SET phone_e164=$2,phone_verified_at=NOW() WHERE id=$1::UUID`, id, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name) VALUES($1::UUID,'Driver legal display')`, owner); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"identity-test-" + owner, "identity-second-" + owner} {
		if _, err = pool.Exec(ctx, `INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1::UUID,$2::UUID,$3,$4,NOW()+INTERVAL '1 hour',NOW()+INTERVAL '1 day')`, uuid.NewString(), owner, gatewayauth.TokenHash(token), uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, id := range []string{owner, other} {
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, id)
		}
	}()
	sender := &contactTestSender{codes: map[string]string{}}
	r := gin.New()
	g := r.Group("", func(c *gin.Context) {
		id := c.GetHeader("X-Test-Owner")
		c.Set("authenticated-user", gatewayauth.User{ID: id, Roles: []string{"rider"}})
	})
	registerRiderIdentityRoutes(g, pool, PhoneAuthConfig{OTPPepper: []byte(strings.Repeat("p", 32)), Sender: sender, EmailSender: sender})
	registerPhoneSessionRoutes(g, pool)
	call := func(method, path, id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Owner", id)
		req.Header.Set("Authorization", "Bearer identity-test-"+id)
		r.ServeHTTP(w, req)
		return w
	}
	if w := call("PATCH", "/rider/profile", owner, `{"displayName":"Rider Ada"}`); w.Code != 200 {
		t.Fatalf("profile: %s", w.Body)
	}
	u, err := gatewayauth.NewPostgresUserStore(pool).ByID(ctx, owner)
	if err != nil || u.RiderName != "Rider Ada" || u.Name != "Driver legal display" {
		t.Fatalf("rider source: %v %v", u, err)
	}
	var driverName string
	if err = pool.QueryRow(ctx, `SELECT display_name FROM driver_profiles WHERE driver_id=$1::UUID`, owner).Scan(&driverName); err != nil || driverName != "Driver legal display" {
		t.Fatal("driver name changed", err)
	}
	if w := call("GET", "/rider/profile", other, ""); strings.Contains(w.Body.String(), "Rider Ada") {
		t.Fatal("foreign profile leaked")
	}
	if w := call("PATCH", "/auth/account", owner, `{"email":"unverified-`+owner+`@example.test"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "email_verification_required") {
		t.Fatalf("unverified linking allowed: %s", w.Body)
	}
	if w := call("POST", "/rider/identity/phone-change/request", owner, `{"destination":"`+foreignPhone+`"}`); w.Code != 409 {
		t.Fatalf("collision: %s", w.Body)
	}
	w := call("POST", "/rider/identity/email-link/request", owner, `{"destination":"identity-`+owner+`@example.test"}`)
	if w.Code != 202 {
		t.Fatalf("email request: %s", w.Body)
	}
	var envelope struct {
		Data struct {
			ChallengeID string `json:"challengeId"`
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	challenge := envelope.Data.ChallengeID
	email := "identity-" + owner + "@example.test"
	// A legacy unverified alias must not become an account link via public sign-in.
	if _, err = pool.Exec(ctx, `UPDATE users SET email=$2,email_verified=FALSE WHERE id=$1::UUID`, owner, email); err != nil {
		t.Fatal(err)
	}
	loginChallenge := uuid.NewString()
	pepper := []byte(strings.Repeat("p", 32))
	if _, err = pool.Exec(ctx, `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at) VALUES($1::UUID,'email',$2,$3,NOW()+INTERVAL '5 minutes')`, loginChallenge, email, otpHash(pepper, loginChallenge, "email:"+email, "123456")); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM auth_otp_challenges WHERE id=$1::UUID`, loginChallenge)
	loginResponse := httptest.NewRecorder()
	loginContext, _ := gin.CreateTestContext(loginResponse)
	loginContext.Request = httptest.NewRequest("POST", "/auth/email/otp/verify", nil)
	(&phoneAuthAPI{pool: pool, users: gatewayauth.NewPostgresUserStore(pool), pepper: pepper}).verifyChallenge(loginContext, "email", email, loginChallenge, "123456", "test")
	if loginResponse.Code != 409 || !strings.Contains(loginResponse.Body.String(), "email_link_required") {
		t.Fatalf("public OTP silently linked alias: %s", loginResponse.Body)
	}
	// Restore the original empty email to keep the already-issued owned proof current.
	if _, err = pool.Exec(ctx, `UPDATE users SET email=NULL WHERE id=$1::UUID`, owner); err != nil {
		t.Fatal(err)
	}
	confirm := `{"challengeId":"` + challenge + `","newCode":"` + sender.codes[email] + `"}`
	if _, err = pool.Exec(ctx, `UPDATE rider_contact_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::UUID`, challenge); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", "/rider/identity/email-link/confirm", owner, confirm); w.Code != 422 {
		t.Fatalf("expired proof accepted: %s", w.Body)
	}
	if _, err = pool.Exec(ctx, `UPDATE rider_contact_challenges SET expires_at=NOW()+INTERVAL '5 minutes' WHERE id=$1::UUID`, challenge); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", "/rider/identity/email-link/confirm", other, confirm); w.Code != 422 {
		t.Fatalf("foreign proof: %s", w.Body)
	}
	if w = call("POST", "/rider/identity/email-link/confirm", owner, confirm); w.Code != 200 {
		t.Fatalf("email confirm: %s", w.Body)
	}
	u, err = gatewayauth.NewPostgresUserStore(pool).ByID(ctx, owner)
	if err != nil || !u.EmailVerified || u.Email != email {
		t.Fatal("email not verified", err)
	}
	w = call("POST", "/rider/identity/phone-change/request", owner, `{"destination":"`+newPhone+`"}`)
	if w.Code != 202 {
		t.Fatalf("phone request: %s", w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	challenge = envelope.Data.ChallengeID
	confirm = `{"challengeId":"` + challenge + `","currentCode":"` + sender.codes[phone] + `","newCode":"` + sender.codes[newPhone] + `"}`
	bad := strings.Replace(confirm, sender.codes[newPhone], "abcdef", 1)
	if w = call("POST", "/rider/identity/phone-change/confirm", owner, bad); w.Code != 422 {
		t.Fatalf("bad proof: %s", w.Body)
	}
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT attempts FROM rider_contact_challenges WHERE id=$1::UUID`, challenge).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("failed proof attempt was not persisted", err)
	}
	var unchanged string
	_ = pool.QueryRow(ctx, `SELECT phone_e164 FROM users WHERE id=$1::UUID`, owner).Scan(&unchanged)
	if unchanged != phone {
		t.Fatal("phone changed without both proofs")
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- call("POST", "/rider/identity/phone-change/confirm", owner, confirm) }()
	}
	winners := 0
	for range 2 {
		w = <-results
		if w.Code == 200 && strings.Contains(w.Body.String(), `"sessionsRevoked":true`) {
			winners++
		} else if w.Code != 422 {
			t.Fatalf("concurrent phone confirm: %s", w.Body)
		}
	}
	if winners != 1 {
		t.Fatalf("phone mutation applied %d times", winners)
	}
	var live int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_sessions WHERE user_id=$1::UUID AND revoked_at IS NULL`, owner).Scan(&live)
	if live != 0 {
		t.Fatal("sessions remained active")
	}
	u, err = gatewayauth.NewPostgresUserStore(pool).ByID(ctx, owner)
	if err != nil || u.Phone != newPhone || !u.PhoneVerified {
		t.Fatal("phone not persisted", err)
	}
	if w = call("POST", "/rider/identity/phone-change/confirm", owner, confirm); w.Code != 422 {
		t.Fatal("consumed proof replay succeeded")
	}
}

func fmtPhone(n uint64) string { return fmt.Sprintf("+2348%09d", n) }
