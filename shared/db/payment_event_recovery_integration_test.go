package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPaymentEventRecoveryMigrationBackfillsOnlyExactLegacyEvidence(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := NewPostgresPool(ctx, Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	txn, rider := "legacy-txn-"+id, "legacy-rider-"+id
	eventID := "SUCCESSFUL_TRANSACTION:" + txn + ":success"
	mismatchID := "REJECTED_PAYMENT:" + txn + ":failed"
	defer pool.Exec(context.Background(), `DELETE FROM payments WHERE id=$1::UUID`, id)
	defer pool.Exec(context.Background(), `DELETE FROM payment_webhook_events WHERE event_id=ANY($1)`, []string{eventID, mismatchID})
	if _, err := pool.Exec(ctx, `INSERT INTO payments(id,trip_id,rider_id,driver_id,amount,currency,status,
		payment_reference,transaction_reference,checkout_url,provider_metadata,created_at,updated_at)
		VALUES($1::UUID,$2,$3,$4,2500,'NGN','success',$5,$6,'https://test.invalid/',
		'{"webhook":{"eventType":"SUCCESSFUL_TRANSACTION"},"private":"must not publish"}',NOW(),NOW())`,
		id, "legacy-trip-"+id, rider, "legacy-driver-"+id, "legacy-pay-"+id, txn); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO payment_webhook_events(event_id,processed_at) VALUES($1,NOW()),($2,NOW())`, eventID, mismatchID); err != nil {
		t.Fatal(err)
	}
	sql, err := migrations.ReadFile("migrations/064_payment_event_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var matched bool
	if err := pool.QueryRow(ctx, `SELECT topic='payment.event.success' AND entity_id=$2
		AND payload->>'eventID'=event_id AND payload->>'transactionReference'=$3
		AND NOT payload ? 'private' AND NOT published FROM payment_webhook_events WHERE event_id=$1`, eventID, rider, txn).Scan(&matched); err != nil || !matched {
		t.Fatalf("legacy envelope not recovered safely: matched=%v err=%v", matched, err)
	}
	var unresolved bool
	if err := pool.QueryRow(ctx, `SELECT payload IS NULL AND topic IS NULL AND NOT published
		FROM payment_webhook_events WHERE event_id=$1`, mismatchID).Scan(&unresolved); err != nil || !unresolved {
		t.Fatalf("mismatched legacy evidence must remain unresolved: %v %v", unresolved, err)
	}
}
