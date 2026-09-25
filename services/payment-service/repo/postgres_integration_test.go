package repo

import (
	"context"
	"os"
	"testing"
	"time"

	paymenttypes "github.com/luxipha/heyGo_backend/services/payment-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/google/uuid"
)

func TestPostgresPaymentRepositoryWebhookIsIdempotent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 2, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	payment := &paymenttypes.Payment{
		ID: id, TripID: "trip-" + id, RiderID: "rider-" + id, DriverID: "driver-" + id,
		Amount: 2500, Currency: "NGN", Status: paymenttypes.PaymentStatusPending,
		PaymentReference: "pay-" + id, TransactionReference: "txn-" + id,
		CheckoutURL: "https://pay.test/" + id, ProviderMetadata: map[string]any{"provider": "monnify"}, CreatedAt: now, UpdatedAt: now,
	}
	repository := NewPostgresPaymentRepository(pool)
	if err := repository.Create(ctx, payment); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	loaded, err := repository.GetByTripID(ctx, payment.TripID)
	if err != nil || loaded.PaymentReference != payment.PaymentReference {
		t.Fatalf("loaded payment=%#v err=%v", loaded, err)
	}
	eventID := "successful:" + id
	updated, publish, err := repository.ApplyWebhook(ctx, WebhookUpdate{
		EventID: eventID, PaymentReference: payment.PaymentReference, TransactionReference: payment.TransactionReference,
		Status: paymenttypes.PaymentStatusSuccess, ProviderMetadata: map[string]any{"verified": true},
	})
	if err != nil || !publish || updated.Status != paymenttypes.PaymentStatusSuccess {
		t.Fatalf("updated=%#v publish=%v err=%v", updated, publish, err)
	}
	if err := repository.MarkEventPublished(ctx, eventID); err != nil {
		t.Fatalf("mark event published: %v", err)
	}
	duplicate, publish, err := repository.ApplyWebhook(ctx, WebhookUpdate{
		EventID: eventID, PaymentReference: payment.PaymentReference, TransactionReference: payment.TransactionReference,
		Status: paymenttypes.PaymentStatusSuccess,
	})
	if err != nil || publish || duplicate != nil {
		t.Fatalf("duplicate=%#v publish=%v err=%v", duplicate, publish, err)
	}
}
