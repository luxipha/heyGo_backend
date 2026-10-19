package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaymentReturnNeverTrustsCheckoutQuery(t *testing.T) {
	w := httptest.NewRecorder()
	PaymentReturn(w, httptest.NewRequest("GET", "/payment/return?status=PAID&transactionReference=private-reference", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "after verification") {
		t.Fatal("missing verification handoff")
	}
	for _, value := range []string{"PAID", "private-reference", "Payment successful"} {
		if strings.Contains(w.Body.String(), value) {
			t.Fatalf("return page trusted checkout input: %s", value)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing privacy headers")
	}
}
