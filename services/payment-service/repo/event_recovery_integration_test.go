package repo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

type recoveryPublisherFunc func(context.Context, string, *contracts.EventMessage, time.Duration) error

func (f recoveryPublisherFunc) SendMessageAndWait(ctx context.Context, topic string, message *contracts.EventMessage, timeout time.Duration) error {
	return f(ctx, topic, message, timeout)
}

func TestPaymentEventRecoveryAfterRestartAndLostAcknowledgement(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	eventID, legacyID := "recovery:"+id, "legacy-unrecoverable:"+id
	defer pool.Exec(context.Background(), `DELETE FROM payment_webhook_events WHERE event_id=ANY($1)`, []string{eventID, legacyID})
	defer pool.Exec(context.Background(), `DELETE FROM payments WHERE id=$1::UUID`, id)
	now := time.Now().UTC()
	payment := &types.Payment{ID: id, TripID: "trip-" + id, RiderID: "rider-" + id, DriverID: "driver-" + id,
		Amount: 2500, Currency: "NGN", Status: types.PaymentStatusPending,
		PaymentReference: "pay-" + id, TransactionReference: "txn-" + id, CheckoutURL: "https://test.invalid/",
		CreatedAt: now, UpdatedAt: now}
	repository := NewPostgresPaymentRepository(pool)
	if err := repository.Create(ctx, payment); err != nil {
		t.Fatal(err)
	}
	update := WebhookUpdate{EventID: eventID, PaymentReference: payment.PaymentReference,
		TransactionReference: payment.TransactionReference, Status: types.PaymentStatusSuccess}
	if _, publish, err := repository.ApplyWebhook(ctx, update); err != nil || !publish {
		t.Fatalf("persist event: publish=%v err=%v", publish, err)
	}
	// The recovery process uses only persisted data, not a live webhook/service.
	if _, err := pool.Exec(ctx, `INSERT INTO payment_webhook_events(event_id,processed_at) VALUES($1,NOW()-INTERVAL '1 hour')`, legacyID); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	var original []byte
	publisher := recoveryPublisherFunc(func(callCtx context.Context, topic string, message *contracts.EventMessage, _ time.Duration) error {
		if message.EventID != eventID {
			return nil
		}
		attempts++
		if topic != contracts.PaymentEventSuccess || message.EntityID != payment.RiderID {
			t.Fatalf("incorrect envelope: %s %#v", topic, message)
		}
		var data messaging.PaymentStatusUpdateData
		if err := json.Unmarshal(message.Data, &data); err != nil || data.EventID != eventID || data.TripID != payment.TripID || data.Status != "success" {
			t.Fatalf("incorrect payload: %#v %v", data, err)
		}
		if attempts == 1 {
			original = append([]byte(nil), message.Data...)
			// A concurrent recovery process must not publish this claimed row.
			if n, err := DrainPaymentEvents(callCtx, pool, recoveryPublisherFunc(func(context.Context, string, *contracts.EventMessage, time.Duration) error {
				t.Fatal("concurrent worker published a claimed row")
				return nil
			}), 100); n != 0 || err != nil {
				t.Fatalf("concurrent drain: n=%d err=%v", n, err)
			}
			return errors.New("acknowledgement lost after publish")
		}
		if string(original) != string(message.Data) {
			t.Fatal("recovery changed the stored event")
		}
		return nil
	})
	if _, err := DrainPaymentEvents(ctx, pool, publisher, 100); err == nil {
		t.Fatal("failed publication should leave an unpublished row")
	}
	var published bool
	if err := pool.QueryRow(ctx, `SELECT published FROM payment_webhook_events WHERE event_id=$1`, eventID).Scan(&published); err != nil || published {
		t.Fatalf("failed publication marked delivered: %v %v", published, err)
	}
	// A later payment change must not rewrite an already persisted event on replay.
	if _, err := pool.Exec(ctx, `UPDATE payments SET status='failed' WHERE id=$1::UUID`, id); err != nil {
		t.Fatal(err)
	}
	replayed, publish, err := NewPostgresPaymentRepository(pool).ApplyWebhook(ctx, update)
	if err != nil || !publish || replayed.Status != types.PaymentStatusSuccess {
		t.Fatalf("immutable webhook replay: %#v %v %v", replayed, publish, err)
	}
	if _, err := DrainPaymentEvents(ctx, pool, publisher, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := DrainPaymentEvents(ctx, pool, publisher, 100); err != nil || attempts != 2 {
		t.Fatalf("duplicate drain: attempts=%d err=%v", attempts, err)
	}
	if err := pool.QueryRow(ctx, `SELECT published FROM payment_webhook_events WHERE event_id=$1`, legacyID).Scan(&published); err != nil || published {
		t.Fatalf("legacy row must remain visible: %v %v", published, err)
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewPaymentBacklogCollector(pool))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "heygo_payment_events_unrecoverable" && family.Metric[0].Gauge.GetValue() >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("unrecoverable legacy row was not exposed to alerting")
	}
}
