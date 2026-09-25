package service

import (
	"context"
	"testing"
	"time"

	"github.com/cprakhar/uber-clone/services/payment-service/repo"
	"github.com/cprakhar/uber-clone/services/payment-service/types"
)

type fakeProcessor struct{ calls int }

func (p *fakeProcessor) CreatePaymentSession(context.Context, int64, string, map[string]string) (*types.ProviderSession, error) {
	p.calls++
	return &types.ProviderSession{
		PaymentReference: "pay-1", TransactionReference: "txn-1", CheckoutURL: "https://checkout.example/1",
	}, nil
}

type fakePaymentRepository struct{ payment *types.Payment }

func (r *fakePaymentRepository) Create(_ context.Context, payment *types.Payment) error {
	r.payment = payment
	return nil
}

func (r *fakePaymentRepository) GetByTripID(_ context.Context, tripID string) (*types.Payment, error) {
	if r.payment == nil || r.payment.TripID != tripID {
		return nil, repo.ErrNotFound
	}
	return r.payment, nil
}

func (r *fakePaymentRepository) ApplyWebhook(context.Context, repo.WebhookUpdate) (*types.Payment, bool, error) {
	return r.payment, true, nil
}

func (r *fakePaymentRepository) MarkEventPublished(context.Context, string) error { return nil }

func TestCreatePaymentSessionReusesTripPayment(t *testing.T) {
	processor := &fakeProcessor{}
	repository := &fakePaymentRepository{}
	svc := NewPaymentService(processor, repository)

	first, err := svc.CreatePaymentSession(context.Background(), "trip-1", "rider-1", "driver-1", 2500, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreatePaymentSession(context.Background(), "trip-1", "rider-1", "driver-1", 2500, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	if processor.calls != 1 {
		t.Fatalf("provider called %d times for duplicate trip command", processor.calls)
	}
	if first.TransactionReference != second.TransactionReference {
		t.Fatal("duplicate command returned a different payment transaction")
	}
	if repository.payment.CreatedAt.After(time.Now()) {
		t.Fatal("payment timestamp is invalid")
	}
}
