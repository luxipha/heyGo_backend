package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestCasperIDVerifierVerifiesBusinessToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "test-key", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
		}}})
	}))
	defer server.Close()

	claims := jwt.MapClaims{
		"iss": "casperid.com", "aud": "heygo-app", "sub": "casper-user-1",
		"exp": time.Now().Add(time.Minute).Unix(), "token_type": "business", "app_id": "heygo-app",
		"humanId": "human-1", "email": "driver@example.com", "email_verified": true, "verified": true, "tier": 2, "trust_score": 640, "scopes": []string{"read:kyc_level"},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	rawToken, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	identity, err := NewCasperIDVerifier(server.URL, "casperid.com", "heygo-app").Verify(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if identity.Subject != "casper-user-1" || identity.HumanID != "human-1" || !identity.Verified || identity.KYCTier != "2" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if identity.TrustScore == nil || *identity.TrustScore != 640 {
		t.Fatalf("trust score was not read from CasperID token: %+v", identity)
	}
	if identity.Email != "driver@example.com" || !identity.EmailVerified {
		t.Fatalf("email was not read from CasperID token: %+v", identity)
	}
}

func TestCasperIDVerifierRejectsWrongAudience(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "test-key", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
		}}})
	}))
	defer server.Close()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "casperid.com", "aud": "another-app", "sub": "user", "exp": time.Now().Add(time.Minute).Unix(),
		"token_type": "business", "app_id": "another-app", "humanId": "human-1",
	})
	token.Header["kid"] = "test-key"
	rawToken, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewCasperIDVerifier(server.URL, "casperid.com", "heygo-app").Verify(context.Background(), rawToken); err == nil {
		t.Fatal("Verify() accepted a token for another application")
	}
}
