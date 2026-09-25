package handler

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"testing"

	"github.com/cprakhar/uber-clone/services/payment-service/types"
	"github.com/cprakhar/uber-clone/shared/contracts"
)

func TestValidMonnifySignature(t *testing.T) {
	body := []byte(`{"eventType":"SUCCESSFUL_TRANSACTION","eventData":{"transactionReference":"MNFY|1"}}`)
	mac := hmac.New(sha512.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	if !ValidMonnifySignature(body, signature, "secret") {
		t.Fatal("expected valid signature")
	}
	if ValidMonnifySignature([]byte(`{"tampered":true}`), signature, "secret") {
		t.Fatal("accepted a signature for a different payload")
	}
	if ValidMonnifySignature(body, "not-hex", "secret") {
		t.Fatal("accepted malformed signature")
	}
}

func TestMonnifyAmountKobo(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
		valid bool
	}{
		{"5000", 500000, true}, {"5000.25", 500025, true}, {"0.01", 1, true},
		{"0", 0, false}, {"1.001", 0, false}, {"-1", 0, false}, {"1e3", 0, false},
	} {
		got, err := monnifyAmountKobo(tc.input)
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Errorf("amount %q: got %d, err %v", tc.input, got, err)
		}
	}
}

func TestWebhookOutcome(t *testing.T) {
	tests := []struct {
		event, providerStatus string
		status                types.PaymentStatus
		topic                 string
	}{
		{"SUCCESSFUL_TRANSACTION", "PAID", types.PaymentStatusSuccess, contracts.PaymentEventSuccess},
		{"REJECTED_PAYMENT", "FAILED", types.PaymentStatusFailed, contracts.PaymentEventFailed},
		{"REJECTED_PAYMENT", "CANCELLED", types.PaymentStatusCancelled, contracts.PaymentEventCancelled},
		{"REJECTED_PAYMENT", "EXPIRED", types.PaymentStatusCancelled, contracts.PaymentEventCancelled},
	}
	for _, test := range tests {
		status, topic, ok := webhookOutcome(test.event, test.providerStatus)
		if !ok || status != test.status || topic != test.topic {
			t.Fatalf("webhookOutcome(%q, %q) = %q, %q, %v", test.event, test.providerStatus, status, topic, ok)
		}
	}
	if _, _, ok := webhookOutcome("SETTLEMENT", "SUCCESS"); ok {
		t.Fatal("unrelated webhook should be ignored")
	}
}
