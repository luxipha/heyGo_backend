package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
)

// PaymentEventDrainHandler requires the pinned Scheduler identity even though
// payment-service has public ingress for provider webhooks.
func PaymentEventDrainHandler(pool *pgxpool.Pool, publisher repo.PaymentEventPublisher, authorize func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || authorize == nil || authorize(r.Context(), strings.TrimPrefix(header, "Bearer ")) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if _, err := repo.DrainPaymentEvents(ctx, pool, publisher, 100); err != nil {
			http.Error(w, "payment event recovery unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
