package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type unresolvedDebtInitializer struct{ calls int }

func (i *unresolvedDebtInitializer) CreateOperatingTopupSession(context.Context, int64, string, string, string) (*types.ProviderSession, error) {
	return nil, errors.New("not used")
}
func (i *unresolvedDebtInitializer) CreateNoShowDebtPaymentSession(context.Context, int64, string, string, string, string) (*types.ProviderSession, error) {
	i.calls++
	return nil, errors.New("provider response interrupted")
}

func TestDebtCheckoutRevalidatesPersistedVerifiedEmail(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	email := "debt-" + owner + "@example.test"
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,roles,email,email_verified) VALUES($1::UUID,ARRAY['rider'],$2,FALSE)`, owner, email); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, owner)
	h := &WebhookHandler{topups: &OperatingTopupHandler{Pool: pool, InternalToken: "test-internal"}}
	selectedDebt := uuid.NewString()
	r := gin.New()
	r.POST("/checkout", h.createNoShowDebtPayment)
	call := func(address string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/checkout", strings.NewReader(`{"riderId":"`+owner+`","debtId":"`+selectedDebt+`","email":"`+address+`","idempotencyKey":"stable"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Token", "test-internal")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := call(email); w.Code != 409 || !strings.Contains(w.Body.String(), "email_not_verified") {
		t.Fatalf("unverified=%s", w.Body)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET email_verified=TRUE WHERE id=$1::UUID`, owner); err != nil {
		t.Fatal(err)
	}
	if w := call("foreign@example.test"); w.Code != 409 || !strings.Contains(w.Body.String(), "email_not_verified") {
		t.Fatalf("stale email=%s", w.Body)
	}
	if w := call(email); w.Code != 409 || !strings.Contains(w.Body.String(), "debt_not_due") {
		t.Fatalf("verified owner gate=%s", w.Body)
	}
	driver, admin1, admin2, policy, fare, trip, claim := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "DEBT_" + trip[:8]
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO users(id,roles) VALUES($1::UUID,ARRAY['driver'])`, driver)
	for _, id := range []string{admin1, admin2} {
		exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$1,'test-only')`, id)
	}
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Test Driver','TEST','sedan','offline')`, driver)
	exec(`INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,5,100000,'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, policy, market, admin1, admin2)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}')`, fare, owner)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,market_code) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'cancelled',$5)`, trip, owner, fare, driver, market)
	exec(`INSERT INTO driver_no_show_claims(id,trip_id,driver_id,rider_id,market_code,policy_id,fee_kobo,arrived_at,eligible_at,status,reviewed_by,reviewed_at) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6::UUID,100000,NOW()-INTERVAL '10 minutes',NOW()-INTERVAL '5 minutes','approved',$7::UUID,NOW())`, claim, trip, driver, owner, market, policy, admin1)
	exec(`INSERT INTO rider_no_show_debts(id,claim_id,rider_id,beneficiary_driver_id,amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,100000,'due')`, selectedDebt, claim, owner, driver)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM rider_no_show_debt_payments WHERE debt_id=$1::UUID`, selectedDebt)
		_, _ = pool.Exec(ctx, `DELETE FROM rider_no_show_debts WHERE id=$1::UUID`, selectedDebt)
		_, _ = pool.Exec(ctx, `DELETE FROM driver_no_show_claims WHERE id=$1::UUID`, claim)
		_, _ = pool.Exec(ctx, `DELETE FROM trips WHERE id=$1::UUID`, trip)
		_, _ = pool.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
		_, _ = pool.Exec(ctx, `DELETE FROM no_show_policies WHERE id=$1::UUID`, policy)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, driver)
		for _, id := range []string{admin1, admin2} {
			_, _ = pool.Exec(ctx, `DELETE FROM admin_users WHERE id=$1::UUID`, id)
		}
	}()
	initializer := &unresolvedDebtInitializer{}
	if w := call(email); w.Code != 503 || !strings.Contains(w.Body.String(), "debt_payment_unavailable") {
		t.Fatalf("unconfigured initializer created an attempt: %s", w.Body)
	}
	h.topups.Initializer = initializer
	if w := call(email); w.Code != 503 || !strings.Contains(w.Body.String(), "checkout_unresolved") {
		t.Fatalf("uncertain provider response=%s", w.Body)
	}
	if w := call(email); w.Code != 201 || !strings.Contains(w.Body.String(), `"status":"pending"`) || !strings.Contains(w.Body.String(), `"checkoutUrl":null`) {
		t.Fatalf("pending checkout recovery=%s", w.Body)
	}
	if initializer.calls != 1 {
		t.Fatal("retry duplicated uncertain provider creation")
	}
	var state string
	if err = pool.QueryRow(ctx, `SELECT status FROM rider_no_show_debt_payments WHERE debt_id=$1::UUID`, selectedDebt).Scan(&state); err != nil || state != "pending" {
		t.Fatal("unobserved outcome did not remain pending", err)
	}
}
