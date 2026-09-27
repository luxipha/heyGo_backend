package messaging

import (
	"context"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DrainOutboxAfter keeps normal API latency predictable while ensuring that
// request-created outbox rows are attempted before Cloud Run can suspend the
// instance. A recovery task remains necessary for crash windows.
func DrainOutboxAfter(next http.Handler, pool *pgxpool.Pool, producer outboxPublisher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if _, err := DrainOutbox(r.Context(), pool, producer, 100); err != nil && !isRequestCancellation(err) {
			log.Printf("outbox request drain failed: %v", err)
		}
	})
}

func OutboxDrainHandler(pool *pgxpool.Pool, producer outboxPublisher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := DrainOutbox(r.Context(), pool, producer, 500); err != nil {
			http.Error(w, "outbox drain failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func isRequestCancellation(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
