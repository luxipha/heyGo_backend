package repo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	paymenttypes "github.com/luxipha/heyGo_backend/services/payment-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
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

func TestPostgresPaymentInitializationClaimIsExclusive(t *testing.T) {
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
	seed := &paymenttypes.Payment{
		ID: id, TripID: "trip-init-" + id, RiderID: "rider-" + id, DriverID: "driver-" + id,
		Amount: 2500, Currency: "NGN", Status: paymenttypes.PaymentStatusPending,
		PaymentReference: "pay-init-" + id, TransactionReference: "initializing-pay-init-" + id,
		CreatedAt: now, UpdatedAt: now,
	}
	repository := NewPostgresPaymentRepository(pool)
	firstToken := uuid.NewString()
	reserved, claimed, err := repository.ClaimSessionInitialization(ctx, seed, firstToken, now.Add(time.Minute))
	if err != nil || !claimed || reserved.TripID != seed.TripID {
		t.Fatalf("first claim payment=%#v claimed=%v err=%v", reserved, claimed, err)
	}
	_, claimed, err = repository.ClaimSessionInitialization(ctx, seed, uuid.NewString(), now.Add(time.Minute))
	if err != nil || claimed {
		t.Fatalf("concurrent claim claimed=%v err=%v", claimed, err)
	}
	completed, err := repository.CompleteSessionInitialization(ctx, seed.TripID, firstToken, &paymenttypes.ProviderSession{
		PaymentReference: seed.PaymentReference, TransactionReference: "txn-init-" + id, CheckoutURL: "https://checkout.test/" + id,
	})
	if err != nil || completed.CheckoutURL == "" {
		t.Fatalf("complete payment=%#v err=%v", completed, err)
	}
	loaded, claimed, err := repository.ClaimSessionInitialization(ctx, seed, uuid.NewString(), now.Add(time.Minute))
	if err != nil || claimed || loaded.TransactionReference != "txn-init-"+id {
		t.Fatalf("completed retry payment=%#v claimed=%v err=%v", loaded, claimed, err)
	}
}
