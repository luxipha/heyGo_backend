package service

import (
	"context"
	"encoding/json"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMonnifyHeroTransfersUseStableReferenceAndExactAmounts(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path == "/api/v1/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]string{"accessToken": "test-token"}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing provider bearer")
		}
		switch r.URL.Path {
		case "/api/v2/disbursements/single":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["reference"] != "hero-stable" || b["amount"] != 7000.25 || b["destinationAccountNumber"] != "0123456789" || b["destinationBankCode"] != "044" || b["sourceAccountNumber"] != "9999999999" || b["async"] != true {
				t.Errorf("incorrect transfer payload %#v", b)
			}
		case "/api/v2/disbursements/single/summary":
			if r.URL.Query().Get("reference") != "hero-stable" {
				t.Error("reference not preserved")
			}
		case "/api/v2/disbursements/single/validate-otp":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["reference"] != "hero-stable" || b["authorizationCode"] != "123456" {
				t.Error("OTP request mismatch")
			}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"reference": "hero-stable", "amount": 7000.25, "status": "PENDING_AUTHORIZATION", "currency": "NGN", "destinationBankCode": "044", "destinationAccountNumber": "0123456789"}})
	}))
	defer server.Close()
	c := NewMonnifyClient(&types.PaymentConfig{BaseURL: server.URL, APIKey: "test", SecretKey: "test"})
	ctx := context.Background()
	if result, err := c.InitiateTransfer(ctx, "hero-stable", 700025, "0123456789", "044", "9999999999"); err != nil || result.Status != "PENDING_AUTHORIZATION" {
		t.Fatal(result, err)
	}
	if result, err := c.TransferStatus(ctx, "hero-stable"); err != nil || result.Amount != "7000.25" {
		t.Fatal(result, err)
	}
	if _, err := c.AuthorizeTransfer(ctx, "hero-stable", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AuthorizeTransfer(ctx, "hero-stable", "bad"); err == nil {
		t.Fatal("invalid OTP accepted")
	}
	if calls["/api/v2/disbursements/single"] != 1 || calls["/api/v2/disbursements/single/summary"] != 1 || calls["/api/v2/disbursements/single/validate-otp"] != 1 {
		t.Fatal("unexpected provider calls", calls)
	}
}
