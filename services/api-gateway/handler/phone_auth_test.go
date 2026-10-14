package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type recordingSMSTransport struct {
	request *http.Request
	body    []byte
}

func (t *recordingSMSTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.request = request
	t.body, _ = io.ReadAll(request.Body)
	return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
}

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"0801 234 5678", "+2348012345678", true},
		{"2348012345678", "+2348012345678", true},
		{"+447700900123", "+447700900123", true},
		{"123", "123", false},
	}
	for _, test := range tests {
		got, ok := normalizePhone(test.input)
		if got != test.want || ok != test.ok {
			t.Fatalf("normalizePhone(%q)=(%q,%t), want (%q,%t)", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{" Driver@Example.COM ", "driver@example.com", true},
		{"not-an-email", "not-an-email", false},
		{"driver@example.com\r\nBcc:x@example.com", "driver@example.com\r\nbcc:x@example.com", false},
	}
	for _, test := range tests {
		got, ok := normalizeEmail(test.input)
		if got != test.want || ok != test.ok {
			t.Fatalf("normalizeEmail(%q)=(%q,%t), want (%q,%t)", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestOTPHashBindsChallengePhoneAndCode(t *testing.T) {
	pepper := []byte("01234567890123456789012345678901")
	base := otpHash(pepper, "challenge", "+2348012345678", "123456")
	for _, changed := range []string{
		otpHash(pepper, "other", "+2348012345678", "123456"),
		otpHash(pepper, "challenge", "+2348099999999", "123456"),
		otpHash(pepper, "challenge", "+2348012345678", "654321"),
	} {
		if changed == base {
			t.Fatal("OTP hash was not bound to all challenge inputs")
		}
	}
}

func TestOTPHashBindsChannel(t *testing.T) {
	pepper := []byte("01234567890123456789012345678901")
	phone := otpHash(pepper, "challenge", "phone:user@example.com", "123456")
	email := otpHash(pepper, "challenge", "email:user@example.com", "123456")
	if phone == email {
		t.Fatal("OTP hash was not bound to the delivery channel")
	}
}

func TestHTTPSMSSenderContract(t *testing.T) {
	transport := &recordingSMSTransport{}
	sender := &HTTPSMSSender{endpoint: "https://sms.example.test/otp", token: "secret", senderID: "HeyGo", client: &http.Client{Transport: transport}}
	if err := sender.SendOTP(context.Background(), "+2348012345678", "123456"); err != nil {
		t.Fatal(err)
	}
	if transport.request == nil || transport.request.Method != http.MethodPost || transport.request.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("unexpected SMS request: %#v", transport.request)
	}
	if string(transport.body) != `{"code":"123456","senderId":"HeyGo","to":"+2348012345678"}` {
		t.Fatalf("unexpected SMS body: %s", transport.body)
	}
}

type recordingEmailOTPSender struct {
	mu    sync.Mutex
	email string
	code  string
}

func (s *recordingEmailOTPSender) SendOTP(_ context.Context, email, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.email, s.code = email, code
	return nil
}

func (s *recordingEmailOTPSender) sent() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.email, s.code
}

func TestEmailOTPLoginCreatesVerifiedAccountAndSession(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	sender := &recordingEmailOTPSender{}
	api := &phoneAuthAPI{
		pool: pool, users: gatewayauth.NewPostgresUserStore(pool),
		pepper: []byte("01234567890123456789012345678901"), emailSender: sender,
	}
	router := gin.New()
	router.POST("/request", api.requestEmailOTP)
	router.POST("/verify", api.verifyEmailOTP)
	router.POST("/refresh", api.refresh)

	email := "otp-" + uuid.NewString() + "@example.com"
	request := httptest.NewRecorder()
	router.ServeHTTP(request, httptest.NewRequest(http.MethodPost, "/request", strings.NewReader(`{"email":"`+strings.ToUpper(email)+`"}`)))
	if request.Code != http.StatusAccepted {
		t.Fatalf("request email OTP: %d %s", request.Code, request.Body.String())
	}
	var requested struct {
		Data struct {
			ChallengeID string `json:"challengeId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request.Body.Bytes(), &requested); err != nil {
		t.Fatal(err)
	}
	sentEmail, code := sender.sent()
	if sentEmail != email || len(code) != 6 || requested.Data.ChallengeID == "" {
		t.Fatalf("unexpected email delivery: email=%q code=%q challenge=%q", sentEmail, code, requested.Data.ChallengeID)
	}

	verify := httptest.NewRecorder()
	verifyBody := fmt.Sprintf(`{"challengeId":%q,"email":%q,"code":%q,"deviceName":"test"}`, requested.Data.ChallengeID, email, code)
	router.ServeHTTP(verify, httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(verifyBody)))
	if verify.Code != http.StatusOK {
		t.Fatalf("verify email OTP: %d %s", verify.Code, verify.Body.String())
	}
	var verified struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(verify.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}
	if verified.Data.AccessToken == "" || verified.Data.RefreshToken == "" {
		t.Fatalf("unexpected verified session: %+v", verified.Data)
	}
	var storedEmail string
	var emailVerified bool
	if err := pool.QueryRow(ctx, `SELECT email,email_verified FROM users WHERE LOWER(email)=$1`, email).Scan(&storedEmail, &emailVerified); err != nil || storedEmail != email || !emailVerified {
		t.Fatalf("email identity was not verified: email=%q verified=%t err=%v", storedEmail, emailVerified, err)
	}

	refresh := httptest.NewRecorder()
	router.ServeHTTP(refresh, httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(fmt.Sprintf(`{"refreshToken":%q}`, verified.Data.RefreshToken))))
	if refresh.Code != http.StatusOK || strings.Contains(refresh.Body.String(), verified.Data.RefreshToken) {
		t.Fatalf("refresh did not rotate the session: %d %s", refresh.Code, refresh.Body.String())
	}
}
