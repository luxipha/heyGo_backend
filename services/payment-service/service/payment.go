package service

import (
	"context"
	"errors"
	"time"

	"github.com/cprakhar/uber-clone/services/payment-service/repo"
	"github.com/cprakhar/uber-clone/services/payment-service/types"
	"github.com/google/uuid"
)

type paymentService struct {
	paymentProcessor repo.PaymentProcessor
	repo             repo.PaymentRepository
}

func NewPaymentService(processor repo.PaymentProcessor, repository repo.PaymentRepository) repo.Service {
	return &paymentService{paymentProcessor: processor, repo: repository}
}

func (s *paymentService) CreatePaymentSession(
	ctx context.Context,
	tripID, riderID, driverID string,
	amount int64,
	currency string) (*types.PaymentIntent, error) {
	if existing, err := s.repo.GetByTripID(ctx, tripID); err == nil {
		return paymentIntent(existing), nil
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}

	metadata := map[string]string{
		"tripID":   tripID,
		"riderID":  riderID,
		"driverID": driverID,
	}

	providerSession, err := s.paymentProcessor.CreatePaymentSession(ctx, amount, currency, metadata)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	payment := &types.Payment{
		ID:                   uuid.New().String(),
		TripID:               tripID,
		RiderID:              riderID,
		DriverID:             driverID,
		Amount:               amount,
		Currency:             currency,
		Status:               types.PaymentStatusPending,
		PaymentReference:     providerSession.PaymentReference,
		TransactionReference: providerSession.TransactionReference,
		CheckoutURL:          providerSession.CheckoutURL,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.repo.Create(ctx, payment); err != nil {
		return nil, err
	}

	return paymentIntent(payment), nil

}

func paymentIntent(payment *types.Payment) *types.PaymentIntent {
	return &types.PaymentIntent{
		ID: payment.ID, TripID: payment.TripID, RiderID: payment.RiderID, DriverID: payment.DriverID,
		Amount: payment.Amount, Currency: payment.Currency,
		PaymentReference: payment.PaymentReference, TransactionReference: payment.TransactionReference,
		CheckoutURL: payment.CheckoutURL, CreatedAt: payment.CreatedAt,
	}
}

func (s *paymentService) ApplyWebhook(ctx context.Context, update repo.WebhookUpdate) (*types.Payment, bool, error) {
	return s.repo.ApplyWebhook(ctx, update)
}

func (s *paymentService) MarkEventPublished(ctx context.Context, eventID string) error {
	return s.repo.MarkEventPublished(ctx, eventID)
}
