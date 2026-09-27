package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
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
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if existing, err := s.repo.GetByTripID(ctx, tripID); err == nil {
		if err := validatePaymentRequest(existing, riderID, driverID, amount, currency); err != nil {
			return nil, err
		}
		if existing.CheckoutURL != "" {
			return paymentIntent(existing), nil
		}
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}
	if tripID == "" || riderID == "" || driverID == "" || amount <= 0 || len(currency) != 3 {
		return nil, fmt.Errorf("valid trip, rider, driver, amount, and currency are required")
	}

	paymentReference := paymentReferenceForTrip(tripID)
	initializationToken := uuid.NewString()
	now := time.Now().UTC()
	reserved := &types.Payment{
		ID:                   uuid.NewString(),
		TripID:               tripID,
		RiderID:              riderID,
		DriverID:             driverID,
		Amount:               amount,
		Currency:             currency,
		Status:               types.PaymentStatusPending,
		PaymentReference:     paymentReference,
		TransactionReference: "initializing-" + paymentReference,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	existing, claimed, err := s.repo.ClaimSessionInitialization(ctx, reserved, initializationToken, now.Add(time.Minute))
	if err != nil {
		return nil, err
	}
	if existing.CheckoutURL != "" {
		return paymentIntent(existing), nil
	}
	if !claimed {
		return nil, fmt.Errorf("payment initialization is already in progress")
	}
	metadata := map[string]string{
		"tripID":   tripID,
		"riderID":  riderID,
		"driverID": driverID,
	}

	providerSession, createErr := s.paymentProcessor.CreatePaymentSession(ctx, paymentReference, amount, currency, metadata)
	if createErr != nil {
		providerSession, err = s.paymentProcessor.RecoverPaymentSession(ctx, paymentReference, amount, currency)
	}
	if err != nil {
		if createErr != nil {
			return nil, fmt.Errorf("initialize payment: %v; recover payment: %w", createErr, err)
		}
		return nil, err
	}
	if providerSession == nil || providerSession.PaymentReference != paymentReference || providerSession.TransactionReference == "" || providerSession.CheckoutURL == "" {
		return nil, fmt.Errorf("payment provider returned an invalid session")
	}
	payment, err := s.repo.CompleteSessionInitialization(ctx, tripID, initializationToken, providerSession)
	if err != nil {
		return nil, err
	}

	return paymentIntent(payment), nil
}

func validatePaymentRequest(payment *types.Payment, riderID, driverID string, amount int64, currency string) error {
	if payment.RiderID != riderID || payment.DriverID != driverID || payment.Amount != amount || payment.Currency != currency {
		return fmt.Errorf("payment request conflicts with the existing trip payment")
	}
	return nil
}

func paymentReferenceForTrip(tripID string) string {
	digest := sha256.Sum256([]byte("heygo-trip-payment-v1:" + tripID))
	return fmt.Sprintf("heygo-trip-%x", digest[:16])
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
