package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"time"
)

// Reconciliation is independent of the Rider reopening the app or webhook
// delivery. The oldest attempted checks rotate through a bounded batch.
func ReconcileTripPaymentsOnce(ctx context.Context, pool *pgxpool.Pool, verifier tripVerifier) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rows, e := pool.Query(ctx, `SELECT payment_reference FROM rider_trip_payments WHERE status='pending' AND transaction_reference IS NOT NULL AND updated_at<NOW()-INTERVAL '30 seconds' ORDER BY updated_at LIMIT 20`)
	if e != nil {
		return e
	}
	refs := []string{}
	for rows.Next() {
		var ref string
		if e = rows.Scan(&ref); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, ref)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, problem := verifyPersistedTripPayment(ctx, pool, verifier, ref)
		if problem != nil {
			slog.Warn("Trip payment reconciliation pending", "code", problem.code)
		}
		if _, e = pool.Exec(ctx, `UPDATE rider_trip_payments SET updated_at=NOW() WHERE payment_reference=$1 AND status='pending'`, ref); e != nil {
			return e
		}
	}
	return nil
}
