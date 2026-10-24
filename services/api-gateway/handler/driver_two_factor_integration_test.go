package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestDriverTwoFactorLogin(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	pepper := strings.Repeat("t", 40)
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
	owner := uuid.NewString()
	email := "twofactor-" + owner + "@example.com"
	phone := "+234" + fmt.Sprintf("%010d", time.Now().UnixNano()%10000000000)
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,phone_e164,email_verified,roles,active) VALUES($1::uuid,$2,$3,TRUE,ARRAY['driver'],TRUE)`, owner, email, phone); err != nil {
		t.Fatal(err)
	}
	users := gatewayauth.NewPostgresUserStore(pool)
	api := phoneAuthAPI{pool: pool, users: users, pepper: []byte(pepper)}
	driver := driverAPI{pool: pool}
	r := gin.New()
	r.POST("/verify", api.verifyEmailOTP)
	r.POST("/phone", api.verifyOTP)
	r.POST("/pin", api.verifyLoginPIN)
	r.POST("/refresh", api.refresh)
	group := r.Group("/", gatewayauth.NewSessionMiddleware(pool, users).Authenticate)
	group.GET("/security", driver.twoFactorStatus)
	group.PUT("/security", driver.setTwoFactor)
	group.PUT("/document-pin", driver.setDocumentPIN)
	tx, _ := pool.Begin(ctx)
	session, err := insertSession(ctx, tx, owner, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var result struct {
			Data map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		return result.Data
	}
	otp := func(channel string) map[string]any {
		t.Helper()
		id := uuid.NewString()
		destination := email
		path := "/verify"
		if channel == "phone" {
			destination = phone
			path = "/phone"
		}
		_, e := pool.Exec(ctx, `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at) VALUES($1::uuid,$2,$3,$4,NOW()+INTERVAL '5 minutes')`, id, channel, destination, otpHash([]byte(pepper), id, channel+":"+destination, "123456"))
		if e != nil {
			t.Fatal(e)
		}
		return call("POST", path, "", fmt.Sprintf(`{"challengeId":%q,%q:%q,"code":"123456","deviceName":"2FA test"}`, id, channel, destination), 200)
	}
	if otp("email")["accessToken"] == nil {
		t.Fatal("disabled 2FA changed normal OTP login")
	}
	status := call("GET", "/security", session.access, "", 200)
	if status["enabled"] != false {
		t.Fatal(status)
	}
	call("PUT", "/security", session.access, `{"enabled":true,"pin":"1234"}`, 409)
	status = call("PUT", "/security", session.access, `{"enabled":true,"newPin":"1234"}`, 200)
	if status["enabled"] != true || status["pinConfigured"] != true {
		t.Fatal(status)
	}
	call("PUT", "/security", session.access, `{"enabled":false,"pin":"0000"}`, 403)
	challenge := otp("email")
	raw, _ := challenge["challengeToken"].(string)
	if challenge["requiresPin"] != true || raw == "" || challenge["accessToken"] != nil || challenge["refreshToken"] != nil {
		t.Fatal("OTP granted full session before PIN")
	}
	call("GET", "/security", raw, "", 401)
	call("POST", "/refresh", "", fmt.Sprintf(`{"refreshToken":%q}`, raw), 401)
	pinBody := func(token, pin string) string { return fmt.Sprintf(`{"challengeToken":%q,"pin":%q}`, token, pin) }
	call("POST", "/pin", "", pinBody(raw, "0000"), 403)
	verified := call("POST", "/pin", "", pinBody(raw, "1234"), 200)
	access, _ := verified["accessToken"].(string)
	if access == "" || verified["refreshToken"] == nil {
		t.Fatal("PIN did not create session")
	}
	call("GET", "/security", access, "", 200)
	call("POST", "/pin", "", pinBody(raw, "1234"), 401)
	raw = otp("phone")["challengeToken"].(string)
	for i := 0; i < 5; i++ {
		want := 403
		if i == 4 {
			want = 429
		}
		call("POST", "/pin", "", pinBody(raw, "0000"), want)
	}
	call("POST", "/pin", "", pinBody(raw, "1234"), 429)
	pool.Exec(ctx, `UPDATE driver_document_pins SET locked_until=NOW()-INTERVAL '1 minute' WHERE driver_id=$1::uuid`, owner)
	pool.Exec(ctx, `UPDATE auth_pin_challenges SET expires_at=NOW()-INTERVAL '1 minute' WHERE token_hash=$1`, gatewayauth.TokenHash(raw))
	call("POST", "/pin", "", pinBody(raw, "1234"), 401)
	// One challenge permits exactly one concurrent session issuance.
	raw = otp("phone")["challengeToken"].(string)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/pin", strings.NewReader(pinBody(raw, "1234")))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[200] != 1 || counts[401] != 1 {
		t.Fatal(counts)
	}
	// Changes use the existing PIN and remain effective for future logins.
	call("PUT", "/document-pin", access, `{"currentPin":"1234","newPin":"5678"}`, 200)
	raw = otp("email")["challengeToken"].(string)
	call("POST", "/pin", "", pinBody(raw, "1234"), 403)
	call("POST", "/pin", "", pinBody(raw, "5678"), 200)
	call("PUT", "/security", access, `{"enabled":false,"pin":"5678"}`, 200)
	if otp("phone")["accessToken"] == nil {
		t.Fatal("disabled 2FA still requests PIN")
	}
	call("PUT", "/security", access, `{"enabled":true,"pin":"5678"}`, 200)
	raw = otp("email")["challengeToken"].(string)
	pool.Exec(ctx, `UPDATE users SET active=FALSE WHERE id=$1::uuid`, owner)
	call("POST", "/pin", "", pinBody(raw, "5678"), 401)
}
