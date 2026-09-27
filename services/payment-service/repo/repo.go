package repo

import (
	"context"
	"errors"
	"time"

	"github.com/luxipha/heyGo_backend/services/payment-service/types"
)

var ErrNotFound = errors.New("payment not found")

type Service interface {
	CreatePaymentSession(ctx context.Context, tripID, riderID, driverID string, amount int64, currency string) (*types.PaymentIntent, error)
	ApplyWebhook(ctx context.Context, update WebhookUpdate) (*types.Payment, bool, error)
	MarkEventPublished(ctx context.Context, eventID string) error
}

type PaymentProcessor interface {
	CreatePaymentSession(ctx context.Context, paymentReference string, amount int64, currency string, metadata map[string]string) (*types.ProviderSession, error)
	RecoverPaymentSession(ctx context.Context, paymentReference string, amount int64, currency string) (*types.ProviderSession, error)
}

type PaymentRepository interface {
	Create(ctx context.Context, payment *types.Payment) error
	GetByTripID(ctx context.Context, tripID string) (*types.Payment, error)
	ClaimSessionInitialization(ctx context.Context, payment *types.Payment, token string, leaseUntil time.Time) (*types.Payment, bool, error)
	CompleteSessionInitialization(ctx context.Context, tripID, token string, session *types.ProviderSession) (*types.Payment, error)
	ApplyWebhook(ctx context.Context, update WebhookUpdate) (*types.Payment, bool, error)
	MarkEventPublished(ctx context.Context, eventID string) error
}

type WebhookUpdate struct {
	EventID              string
	PaymentReference     string
	TransactionReference string
	Status               types.PaymentStatus
	ProviderMetadata     map[string]any
}
