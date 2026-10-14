package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaymentRecoveryRejectsUnauthenticatedAndWrongIdentity(t *testing.T) {
	for _, header := range []string{"", "Basic token", "Bearer attacker"} {
		req := httptest.NewRequest(http.MethodPost, "/internal/tasks/payment-events/drain", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		PaymentEventDrainHandler(nil, nil, func(context.Context, string) error {
			return errors.New("unexpected identity")
		}).ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status=%d", header, w.Code)
		}
	}
	w := httptest.NewRecorder()
	PaymentEventDrainHandler(nil, nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/tasks/payment-events/drain", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Fatal("recovery must reject GET")
	}
}
