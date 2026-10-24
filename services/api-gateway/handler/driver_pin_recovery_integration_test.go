package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"golang.org/x/crypto/bcrypt"
)

type recoverySender struct {
	code string
	sent int
}

func (s *recoverySender) SendOTP(_ context.Context, _, code string) error {
	s.code = code
	s.sent++
	return nil
}

func TestDriverPINRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	pepper := strings.Repeat("r", 40)
	t.Setenv("OTP_PEPPER", pepper)
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	owner, other := uuid.NewString(), uuid.NewString()
	email := "reset-" + owner + "@example.com"
	phone := "+234" + time.Now().Format("1504050000")
	exec(`INSERT INTO users(id,email,email_verified,phone_e164,phone_verified_at,roles,driver_two_factor_enabled) VALUES($1::uuid,$2,TRUE,$3,NOW(),ARRAY['driver'],TRUE)`, owner, email, phone)
	exec(`INSERT INTO users(id,email,email_verified,roles) VALUES($1::uuid,$2,FALSE,ARRAY['driver'])`, other, "unverified-"+other+"@example.com")
	digest, _ := documentPINDigest(owner, "1234")
	hash, _ := bcrypt.GenerateFromPassword([]byte(digest), bcrypt.DefaultCost)
	exec(`INSERT INTO driver_document_pins(driver_id,pin_hash,failed_attempts,locked_until) VALUES($1::uuid,$2,5,NOW()+INTERVAL '15 minutes')`, owner, string(hash))
	tx, _ := pool.Begin(ctx)
	session, e := insertSession(ctx, tx, owner, "old phone")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	pending, _ := randomToken()
	exec(`INSERT INTO auth_pin_challenges(token_hash,user_id,expires_at) VALUES($1,$2::uuid,NOW()+INTERVAL '5 minutes')`, gatewayauth.TokenHash(pending), owner)
	// Multiple device records must be retained and revoked without unique-token collisions.
	for i := 0; i < 2; i++ {
		id := uuid.NewString()
		exec(`INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::uuid,$2::uuid,'ios',$1)`, id, owner)
	}
	sender := &recoverySender{}
	api := phoneAuthAPI{pool: pool, users: gatewayauth.NewPostgresUserStore(pool), pepper: []byte(pepper), emailSender: sender, smsSender: sender}
	r := gin.New()
	r.POST("/request", api.requestPINRecovery)
	r.POST("/reset", api.resetPIN)
	r.POST("/login", api.verifyEmailOTP)
	r.POST("/pin", api.verifyLoginPIN)
	call := func(path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s = %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &envelope)
		return envelope.Data
	}
	request := func(channel, destination string) string {
		t.Helper()
		return call("/request", gin.H{"channel": channel, "destination": destination}, 202)["challengeId"].(string)
	}
	unknown := request("email", "unknown-"+owner+"@example.com")
	if sender.sent != 0 {
		t.Fatal("unknown contact received OTP")
	}
	call("/reset", gin.H{"challengeId": unknown, "code": "123456", "newPin": "4567"}, 401)
	request("email", "unverified-"+other+"@example.com")
	if sender.sent != 0 {
		t.Fatal("unverified contact received OTP")
	}
	id := request("email", email)
	code := sender.code
	if sender.sent != 1 {
		t.Fatal("OTP was not delivered")
	}
	// A recovery code cannot be used as a login OTP, and a login OTP cannot reset PIN.
	call("/login", gin.H{"email": email, "challengeId": id, "code": code}, 401)
	login := uuid.NewString()
	exec(`INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at) VALUES($1::uuid,'email',$2,$3,NOW()+INTERVAL '5 minutes')`, login, email, otpHash([]byte(pepper), login, "email:"+email, "123456"))
	call("/reset", gin.H{"challengeId": login, "code": "123456", "newPin": "4567"}, 401)
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	call("/reset", gin.H{"challengeId": id, "code": wrong, "newPin": "4567"}, 401)
	result := call("/reset", gin.H{"challengeId": id, "code": code, "newPin": "4567"}, 200)
	if result["reset"] != true || result["signInRequired"] != true || result["accessToken"] != nil {
		t.Fatal("reset granted full login")
	}
	call("/reset", gin.H{"challengeId": id, "code": code, "newPin": "9999"}, 401)
	call("/pin", gin.H{"challengeToken": pending, "pin": "4567"}, 401)
	var enabled, revoked bool
	var attempts, devices, notes int
	var locked *time.Time
	var saved string
	if e = pool.QueryRow(ctx, `SELECT driver_two_factor_enabled FROM users WHERE id=$1::uuid`, owner).Scan(&enabled); e != nil || !enabled {
		t.Fatal("2FA disabled", e)
	}
	if e = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM user_sessions WHERE access_token_hash=$1`, gatewayauth.TokenHash(session.access)).Scan(&revoked); e != nil || !revoked {
		t.Fatal("session not revoked", e)
	}
	if e = pool.QueryRow(ctx, `SELECT pin_hash,failed_attempts,locked_until FROM driver_document_pins WHERE driver_id=$1::uuid`, owner).Scan(&saved, &attempts, &locked); e != nil {
		t.Fatal(e)
	}
	newDigest, _ := documentPINDigest(owner, "4567")
	if bcrypt.CompareHashAndPassword([]byte(saved), []byte(newDigest)) != nil || attempts != 0 || locked != nil {
		t.Fatal("PIN/lockout not reset")
	}
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_devices WHERE driver_id=$1::uuid AND revoked_at IS NOT NULL`, owner).Scan(&devices)
	if devices != 2 {
		t.Fatal("devices not retained and revoked")
	}
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::uuid AND source_event_id=$2`, owner, "pin-reset:"+id).Scan(&notes)
	if notes != 1 {
		t.Fatal("security notice missing")
	}
	// Phone recovery uses the verified phone and the same account, with bounded attempts.
	phoneID := request("phone", phone)
	phoneCode := sender.code
	bad := "000000"
	if bad == phoneCode {
		bad = "111111"
	}
	for i := 0; i < 5; i++ {
		call("/reset", gin.H{"challengeId": phoneID, "code": bad, "newPin": "8888"}, 401)
	}
	call("/reset", gin.H{"challengeId": phoneID, "code": phoneCode, "newPin": "8888"}, 401)
	expired := request("phone", phone)
	exec(`UPDATE auth_otp_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, expired)
	call("/reset", gin.H{"challengeId": expired, "code": sender.code, "newPin": "8888"}, 401)
	// Durable request limit is shared with normal OTP requests for the destination.
	request("phone", phone)
	call("/request", gin.H{"channel": "phone", "destination": phone}, 429)
}
