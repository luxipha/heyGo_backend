package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
)

func TestRiderDebtCheckoutRequiresVerifiedEmailBeforeProviderCall(t *testing.T) {
	for _, test := range []struct {
		name     string
		email    string
		verified bool
		key      string
		code     string
	}{
		{"unverified", "rider@example.test", false, "stable-checkout-key", "email_not_verified"},
		{"invalid verified address", "invalid", true, "stable-checkout-key", "email_required"},
		{"missing idempotency", "rider@example.test", true, "", "idempotency_key_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := gin.New()
			g := r.Group("/rider", func(c *gin.Context) {
				c.Set("authenticated-user", gatewayauth.User{ID: uuid.NewString(), Email: test.email, EmailVerified: test.verified, Roles: []string{"rider"}})
			})
			registerRiderNoShowPaymentRoutes(g, nil)
			req := httptest.NewRequest(http.MethodPost, "/rider/no-show-debts/"+uuid.NewString()+"/pay", nil)
			req.Header.Set("Idempotency-Key", test.key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if !strings.Contains(w.Body.String(), test.code) || (w.Code != http.StatusConflict && w.Code != http.StatusBadRequest) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRiderDebtCheckoutForwardsOwnerAndStableKey(t *testing.T) {
	owner, debt := uuid.NewString(), uuid.NewString()
	providerCalls := 0
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["riderId"] != owner || body["debtId"] != debt || body["idempotencyKey"] != "stable-key" || body["email"] != "rider@example.test" {
			t.Errorf("unexpected owned checkout request: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"pending-checkout","status":"pending"}}`))
	}))
	defer service.Close()
	t.Setenv("PAYMENT_SERVICE_URL", service.URL)
	t.Setenv("PAYMENT_SERVICE_AUDIENCE", "")
	r := gin.New()
	g := r.Group("/rider", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: owner, Email: "rider@example.test", EmailVerified: true, Roles: []string{"rider"}})
	})
	registerRiderNoShowPaymentRoutes(g, nil)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/rider/no-show-debts/"+debt+"/pay", strings.NewReader(`{"riderId":"foreign","status":"paid"}`))
		req.Header.Set("Idempotency-Key", "stable-key")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"status":"pending"`) {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if providerCalls != 2 {
		t.Fatalf("checkout service requests=%d", providerCalls)
	}
}
