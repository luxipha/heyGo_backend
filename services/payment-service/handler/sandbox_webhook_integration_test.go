package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type sandboxTestVerifier struct {
	result *service.VerifiedTransaction
	err    error
	calls  int
}

func (v *sandboxTestVerifier) VerifyTransaction(context.Context, string) (*service.VerifiedTransaction, error) {
	v.calls++
	return v.result, v.err
}
func TestSandboxNotificationDatabaseIdentity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 2, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	ref := "sandbox-test-" + id
	txn := "sandbox-txn-" + id
	_, err = pool.Exec(ctx, `INSERT INTO payments(id,trip_id,rider_id,driver_id,amount,currency,status,payment_reference,transaction_reference,checkout_url,created_at,updated_at) VALUES($1,$2,'r','d',1000,'NGN','pending',$2,$3,'https://sandbox.test',NOW(),NOW())`, id, ref, txn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM payments WHERE id=$1`, id)
	v := &sandboxTestVerifier{result: &service.VerifiedTransaction{PaymentReference: ref, TransactionReference: txn, PaymentStatus: "PAID", CurrencyCode: "NGN", TotalPayable: "10.00", AmountPaid: "10.00"}}
	h := &WebhookHandler{topups: &OperatingTopupHandler{Pool: pool, Verifier: v}}
	event := monnifyWebhook{EventType: "SUCCESSFUL_TRANSACTION"}
	event.EventData.PaymentReference = ref
	event.EventData.TransactionReference = txn
	event.EventData.PaymentStatus = "PAID"
	check := func(e monnifyWebhook, want bool, code int) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/webhooks/moniepoint", nil)
		if got := h.verifySandboxNotification(c, &e); got != want || (!want && w.Code != code) {
			t.Fatalf("verified=%v http=%d want=%v/%d", got, w.Code, want, code)
		}
	}
	check(event, true, 200)
	v.result.AmountPaid = "9.00"
	check(event, false, 409)
	v.result.AmountPaid = "10.00"
	v.err = errors.New("provider offline")
	check(event, false, 503)
	v.err = nil
	calls := v.calls
	unknown := event
	unknown.EventData.PaymentReference = "missing-" + id
	check(unknown, false, 404)
	mismatch := event
	mismatch.EventData.TransactionReference = "wrong"
	check(mismatch, false, 409)
	if v.calls != calls {
		t.Fatal("unknown/mismatched payment triggered provider request")
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM payments WHERE id=$1`, id).Scan(&status); err != nil || status != "pending" {
		t.Fatal("verification gate changed payment state")
	}
}
