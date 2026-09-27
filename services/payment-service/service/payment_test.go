package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
)

type fakeProcessor struct {
	calls        int
	recoverCalls int
	createErr    error
}

func (p *fakeProcessor) CreatePaymentSession(_ context.Context, paymentReference string, _ int64, _ string, _ map[string]string) (*types.ProviderSession, error) {
	p.calls++
	if p.createErr != nil {
		return nil, p.createErr
	}
	return &types.ProviderSession{
		PaymentReference: paymentReference, TransactionReference: "txn-1", CheckoutURL: "https://checkout.example/1",
	}, nil
}

func (p *fakeProcessor) RecoverPaymentSession(_ context.Context, paymentReference string, _ int64, _ string) (*types.ProviderSession, error) {
	p.recoverCalls++
	return &types.ProviderSession{PaymentReference: paymentReference, TransactionReference: "txn-recovered", CheckoutURL: "https://checkout.example/recovered"}, nil
}

type fakePaymentRepository struct {
	payment *types.Payment
	token   string
}

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

func (r *fakePaymentRepository) ClaimSessionInitialization(_ context.Context, payment *types.Payment, token string, _ time.Time) (*types.Payment, bool, error) {
	if r.payment != nil {
		return r.payment, r.payment.CheckoutURL == "" && r.token == token, nil
	}
	copy := *payment
	r.payment = &copy
	r.token = token
	return r.payment, true, nil
}

func (r *fakePaymentRepository) CompleteSessionInitialization(_ context.Context, tripID, token string, session *types.ProviderSession) (*types.Payment, error) {
	if r.payment == nil || r.payment.TripID != tripID || r.token != token {
		return nil, errors.New("initialization claim mismatch")
	}
	r.payment.PaymentReference = session.PaymentReference
	r.payment.TransactionReference = session.TransactionReference
	r.payment.CheckoutURL = session.CheckoutURL
	return r.payment, nil
}

func (r *fakePaymentRepository) ApplyWebhook(context.Context, repo.WebhookUpdate) (*types.Payment, bool, error) {
	return r.payment, true, nil
}

func TestCreatePaymentSessionRecoversUncertainProviderCall(t *testing.T) {
	processor := &fakeProcessor{createErr: errors.New("response lost")}
	repository := &fakePaymentRepository{}
	svc := NewPaymentService(processor, repository)

	session, err := svc.CreatePaymentSession(context.Background(), "trip-recover", "rider-1", "driver-1", 2500, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	if processor.calls != 1 || processor.recoverCalls != 1 {
		t.Fatalf("create calls=%d recover calls=%d", processor.calls, processor.recoverCalls)
	}
	if session.TransactionReference != "txn-recovered" || session.PaymentReference != paymentReferenceForTrip("trip-recover") {
		t.Fatalf("unexpected recovered session: %#v", session)
	}
}

func TestPaymentReferenceForTripIsStableAndScoped(t *testing.T) {
	first := paymentReferenceForTrip("trip-1")
	if first != paymentReferenceForTrip("trip-1") {
		t.Fatal("payment reference changed for the same trip")
	}
	if first == paymentReferenceForTrip("trip-2") {
		t.Fatal("different trips received the same payment reference")
	}
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
