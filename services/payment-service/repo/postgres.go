package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cprakhar/uber-clone/services/payment-service/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresPaymentRepository struct{ pool *pgxpool.Pool }

func NewPostgresPaymentRepository(pool *pgxpool.Pool) PaymentRepository {
	return &postgresPaymentRepository{pool: pool}
}

func (r *postgresPaymentRepository) Create(ctx context.Context, payment *types.Payment) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payments (
			id, trip_id, rider_id, driver_id, amount, currency, status,
			payment_reference, transaction_reference, checkout_url, provider_metadata,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		payment.ID, payment.TripID, payment.RiderID, payment.DriverID, payment.Amount,
		payment.Currency, payment.Status, payment.PaymentReference, payment.TransactionReference,
		payment.CheckoutURL, payment.ProviderMetadata, payment.CreatedAt, payment.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("persist payment: %w", err)
	}
	return nil
}

func (r *postgresPaymentRepository) GetByTripID(ctx context.Context, tripID string) (*types.Payment, error) {
	payment, err := scanPayment(r.pool.QueryRow(ctx, paymentSelect+` WHERE trip_id = $1`, tripID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load payment: %w", err)
	}
	return payment, nil
}

func (r *postgresPaymentRepository) ApplyWebhook(ctx context.Context, update WebhookUpdate) (*types.Payment, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin webhook transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, update.EventID); err != nil {
		return nil, false, fmt.Errorf("lock webhook event: %w", err)
	}
	var published bool
	err = tx.QueryRow(ctx, `
		INSERT INTO payment_webhook_events (event_id, processed_at)
		VALUES ($1, $2)
		ON CONFLICT (event_id) DO UPDATE SET event_id = EXCLUDED.event_id
		RETURNING published`, update.EventID, time.Now().UTC()).Scan(&published)
	if err != nil {
		return nil, false, fmt.Errorf("claim webhook event: %w", err)
	}
	if published {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit duplicate webhook: %w", err)
		}
		return nil, false, nil
	}

	metadata, err := json.Marshal(update.ProviderMetadata)
	if err != nil {
		return nil, false, fmt.Errorf("encode provider metadata: %w", err)
	}
	payment, err := scanPayment(tx.QueryRow(ctx, paymentSelect+`
		WHERE (payment_reference = $1 OR transaction_reference = $2)
		  AND status IN ('pending', $3)
		FOR UPDATE`, update.PaymentReference, update.TransactionReference, update.Status))
	if errors.Is(err, pgx.ErrNoRows) {
		payment, err = scanPayment(tx.QueryRow(ctx, paymentSelect+`
			WHERE payment_reference = $1 OR transaction_reference = $2
			FOR UPDATE`, update.PaymentReference, update.TransactionReference))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		if err != nil {
			return nil, false, fmt.Errorf("load terminal payment: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_webhook_events SET published = TRUE WHERE event_id = $1`, update.EventID); err != nil {
			return nil, false, fmt.Errorf("finalize ignored webhook: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit ignored webhook: %w", err)
		}
		return payment, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock payment: %w", err)
	}

	payment, err = scanPayment(tx.QueryRow(ctx, paymentSelect+`
		WHERE id = $1`, payment.ID))
	if err != nil {
		return nil, false, fmt.Errorf("reload payment: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE payments SET status = $1, transaction_reference = $2,
			provider_metadata = $3, updated_at = $4
		WHERE id = $5`, update.Status, update.TransactionReference, metadata, time.Now().UTC(), payment.ID)
	if err != nil {
		return nil, false, fmt.Errorf("persist webhook payment status: %w", err)
	}
	payment.Status = update.Status
	payment.TransactionReference = update.TransactionReference
	payment.ProviderMetadata = update.ProviderMetadata
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit webhook update: %w", err)
	}
	return payment, true, nil
}

func (r *postgresPaymentRepository) MarkEventPublished(ctx context.Context, eventID string) error {
	result, err := r.pool.Exec(ctx, `UPDATE payment_webhook_events SET published = TRUE WHERE event_id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("mark webhook event published: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const paymentSelect = `SELECT id, trip_id, rider_id, driver_id, amount, currency, status,
	payment_reference, transaction_reference, checkout_url, provider_metadata, created_at, updated_at
	FROM payments`

type rowScanner interface{ Scan(...any) error }

func scanPayment(row rowScanner) (*types.Payment, error) {
	var payment types.Payment
	var status string
	var metadata []byte
	err := row.Scan(
		&payment.ID, &payment.TripID, &payment.RiderID, &payment.DriverID, &payment.Amount,
		&payment.Currency, &status, &payment.PaymentReference, &payment.TransactionReference,
		&payment.CheckoutURL, &metadata, &payment.CreatedAt, &payment.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	payment.Status = types.PaymentStatus(status)
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &payment.ProviderMetadata); err != nil {
			return nil, fmt.Errorf("decode provider metadata: %w", err)
		}
	}
	return &payment, nil
}
