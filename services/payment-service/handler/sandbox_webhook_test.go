package handler

import (
	"encoding/json"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSandboxCallbackRequiresExactSandboxOriginAndMissingSignature(t *testing.T) {
	body := `{"eventType":"SUCCESSFUL_TRANSACTION","eventData":{"paymentReference":"p","transactionReference":"t","paymentStatus":"PAID"}}`
	for _, tc := range []struct {
		origin, signature string
		want              int
	}{
		{"https://api.monnify.com", "", 401},
		{"", "", 401},
		{"https://sandbox.monnify.com.evil.example", "", 401},
		{"http://sandbox.monnify.com", "", 401},
		{"https://sandbox.monnify.com", "invalid", 401},
		{"https://sandbox.monnify.com", "", 503},
	} {
		t.Run(tc.origin+tc.signature, func(t *testing.T) {
			router := NewHTTPHandlerWithTopups("secret", nil, nil, &OperatingTopupHandler{MonnifyBaseURL: tc.origin})
			req := httptest.NewRequest("POST", "/webhooks/moniepoint", strings.NewReader(body))
			if tc.signature != "" {
				req.Header.Set("monnify-signature", tc.signature)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestSandboxPaymentRequiresMatchingProviderEvidence(t *testing.T) {
	var event monnifyWebhook
	if err := json.Unmarshal([]byte(`{"eventType":"SUCCESSFUL_TRANSACTION","eventData":{"paymentReference":"p","transactionReference":"t","paymentStatus":"PAID"}}`), &event); err != nil {
		t.Fatal(err)
	}
	valid := service.VerifiedTransaction{PaymentReference: "p", TransactionReference: "t", PaymentStatus: "PAID", CurrencyCode: "NGN", TotalPayable: "10.00", AmountPaid: "10.00"}
	if !matchesSandboxPayment(event, &valid, 1000, "NGN") {
		t.Fatal("valid evidence rejected")
	}
	changes := []func(*service.VerifiedTransaction){
		func(v *service.VerifiedTransaction) { v.PaymentReference = "other" },
		func(v *service.VerifiedTransaction) { v.TransactionReference = "other" },
		func(v *service.VerifiedTransaction) { v.CurrencyCode = "USD" },
		func(v *service.VerifiedTransaction) { v.PaymentStatus = "PENDING" },
		func(v *service.VerifiedTransaction) { v.TotalPayable = "11.00" },
		func(v *service.VerifiedTransaction) { v.AmountPaid = "9.00" },
		func(v *service.VerifiedTransaction) { v.TotalPayable = "invalid" },
	}
	for i, change := range changes {
		v := valid
		change(&v)
		if matchesSandboxPayment(event, &v, 1000, "NGN") {
			t.Fatalf("accepted mismatch %d", i)
		}
	}
	if matchesSandboxPayment(event, nil, 1000, "NGN") {
		t.Fatal("accepted nil verification")
	}
	event.EventType = "SUCCESSFUL_REFUND"
	if matchesSandboxPayment(event, &valid, 1000, "NGN") {
		t.Fatal("accepted unsigned refund")
	}
}

func TestSandboxCannotTurnPendingIntoSuccessOrPaidIntoFailure(t *testing.T) {
	for _, tc := range []struct {
		event, status string
		want          bool
	}{
		{"SUCCESSFUL_TRANSACTION", "PENDING", false}, {"REJECTED_PAYMENT", "PAID", false},
		{"REJECTED_PAYMENT", "PENDING", false}, {"SUCCESSFUL_TRANSACTION", "PAID", true},
		{"REJECTED_PAYMENT", "FAILED", true}, {"REJECTED_PAYMENT", "CANCELLED", true}, {"REJECTED_PAYMENT", "EXPIRED", true},
	} {
		var e monnifyWebhook
		e.EventType = tc.event
		e.EventData.PaymentStatus = tc.status
		if sandboxOutcomeAllowed(e) != tc.want {
			t.Fatalf("incorrect outcome %s %s", tc.event, tc.status)
		}
	}
}
