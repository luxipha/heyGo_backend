package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type tripPaymentProvider struct {
	mu               sync.Mutex
	calls            int
	ref, txn, status string
	paid             int64
}

func (p *tripPaymentProvider) CreatePaymentSession(_ context.Context, ref string, _ int64, _ string, meta map[string]string) (*types.ProviderSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.ref = ref
	p.txn = "MNFY-" + ref
	if !strings.HasSuffix(meta["customerEmail"], "@example.com") {
		return nil, fmt.Errorf("verified email not forwarded")
	}
	return &types.ProviderSession{PaymentReference: ref, TransactionReference: p.txn, CheckoutURL: "https://sandbox.sdk.monnify.com/checkout/test"}, nil
}
func (p *tripPaymentProvider) RecoverPaymentSession(context.Context, string, int64, string) (*types.ProviderSession, error) {
	return nil, fmt.Errorf("no recovery expected")
}
func (p *tripPaymentProvider) VerifyTransaction(context.Context, string) (*service.VerifiedTransaction, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &service.VerifiedTransaction{PaymentReference: p.ref, TransactionReference: p.txn, PaymentStatus: p.status, CurrencyCode: "NGN", AmountPaid: json.Number(fmt.Sprintf("%d.%02d", p.paid/100, p.paid%100)), TotalPayable: "100.00"}, nil
}

func TestMonnifyTripCollectionExactlyOnce(t *testing.T) {
	dsn := os.Getenv("MONNIFY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable Monnify database required")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	rider, driver, other, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{rider, driver, other} {
		exec(`INSERT INTO users(id,roles,email,email_verified)VALUES($1::uuid,ARRAY['rider','driver'],$2,true)`, id, id+"@example.com")
	}
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed')`, trip, rider, driver, fare)
	exec(`INSERT INTO trip_settlements(trip_id,rider_id,driver_id,status,expected_amount_kobo,fare_amount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,'pending',10000,10000)`, trip, rider, driver)
	// A completed ride already spent 2,500 kobo of ride credit and also
	// collects an approved 2,500-kobo no-show debt for a different Driver.
	creator, approver, policy, oldFare, oldTrip, claim, debt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, id)
	}
	exec(`INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,($1::uuid)::text,1,3,2500,'approved',NOW()-INTERVAL '1 day',$2::uuid,$3::uuid,NOW())`, policy, creator, approver)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, oldFare, rider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'cancelled')`, oldTrip, rider, other, oldFare)
	exec(`INSERT INTO driver_no_show_claims(id,trip_id,driver_id,rider_id,market_code,policy_id,fee_kobo,arrived_at,eligible_at,status,reviewed_by,reviewed_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,($5::uuid)::text,$5::uuid,2500,NOW()-INTERVAL '1 hour',NOW()-INTERVAL '50 minutes','approved',$6::uuid,NOW())`, claim, oldTrip, other, rider, policy, approver)
	exec(`INSERT INTO rider_no_show_debts(id,claim_id,rider_id,beneficiary_driver_id,amount_kobo,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,2500,'due')`, debt, claim, rider, other)
	exec(`INSERT INTO trip_no_show_debt_settlements(trip_id,debt_id,beneficiary_driver_id,amount_kobo,status)VALUES($1::uuid,$2::uuid,$3::uuid,2500,'pending')`, trip, debt, other)
	exec(`UPDATE trips SET ride_credit_kobo=2500,no_show_debt_kobo=2500 WHERE id=$1::uuid`, trip)
	exec(`UPDATE trip_settlements SET ride_credit_kobo=2500,no_show_debt_kobo=2500 WHERE trip_id=$1::uuid`, trip)
	exec(`INSERT INTO driver_operating_accounts(driver_id,balance_kobo)VALUES($1::uuid,2500)`, driver)
	key := []byte("01234567890123456789012345678901")
	sealed := func(value, purpose string) []byte {
		v, e := monnifysettings.Seal(key, value, purpose)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	exec(`INSERT INTO admin_monnify_settings(enabled,trip_payments_enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext)VALUES(true,true,'sandbox',$1,$2,$3)`, sealed("api-one", "api-key"), sealed("secret-one", "secret-key"), sealed("contract-one", "contract-code"))
	provider := &tripPaymentProvider{status: "PENDING"}
	router := NewHTTPHandlerWithTopups("test-secret", nil, nil, &OperatingTopupHandler{Pool: pool, InternalToken: "test-internal", MonnifyBaseURL: "https://sandbox.monnify.com", TripInitializer: provider, Verifier: provider})
	call := func(path, body, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Internal-Service-Token", token)
		router.ServeHTTP(w, r)
		return w
	}
	checkout := fmt.Sprintf(`{"tripId":%q,"riderId":%q}`, trip, rider)
	if w := call("/internal/trip-payments/checkout", checkout, ""); w.Code != 401 {
		t.Fatal("internal auth", w.Body.String())
	}
	if w := call("/internal/trip-payments/checkout", fmt.Sprintf(`{"tripId":%q,"riderId":%q}`, trip, other), "test-internal"); w.Code != 404 {
		t.Fatal("foreign trip", w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := call("/internal/trip-payments/checkout", checkout, "test-internal"); w.Code != 200 {
			t.Fatal("checkout", w.Body.String())
		}
	}
	if provider.calls != 1 {
		t.Fatal("duplicate initialization", provider.calls)
	}
	// Rotate settings: the original order must retain its original credentials.
	exec(`INSERT INTO admin_monnify_settings(enabled,trip_payments_enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext)VALUES(true,true,'sandbox',$1,$2,$3)`, sealed("api-two", "api-key"), sealed("secret-two", "secret-key"), sealed("contract-two", "contract-code"))
	configured := &service.ConfiguredMonnify{Pool: pool, Key: key, Fallback: &types.PaymentConfig{RedirectURL: "https://heygo.test/payment/return"}}
	pinned, e := configured.Config(ctx, provider.ref)
	if e != nil || pinned.APIKey != "api-one" {
		t.Fatal("credential pinning", e)
	}
	current, e := configured.Config(ctx, "")
	if e != nil || current.APIKey != "api-two" {
		t.Fatal("current credentials", e)
	}
	event := fmt.Sprintf(`{"eventType":"SUCCESSFUL_TRANSACTION","eventData":{"paymentReference":%q,"transactionReference":%q,"paymentStatus":"PAID"}}`, provider.ref, provider.txn)
	if w := call("/webhooks/moniepoint", event, ""); w.Code != 200 {
		t.Fatal("pending verification", w.Body.String())
	}
	var n int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM driver_operating_entries WHERE source_key=$1`, "trip-collection:"+trip+":"+driver).Scan(&n); e != nil || n != 0 {
		t.Fatal("unsigned payload must not prove payment", n, e)
	}
	provider.mu.Lock()
	provider.status = "PAID"
	provider.paid = 9999
	provider.mu.Unlock()
	if w := call("/internal/trip-payments/refresh", checkout, "test-internal"); w.Code != 409 {
		t.Fatal("amount mismatch", w.Body.String())
	}
	provider.mu.Lock()
	provider.paid = 10000
	provider.mu.Unlock()
	codes := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- call("/webhooks/moniepoint", event, "").Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("duplicate posting", code)
		}
	}
	var balance int64
	if e = pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid`, driver).Scan(&balance); e != nil || balance != 10000 {
		t.Fatal("driver credit", balance, e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM driver_operating_entries WHERE source_key=$1`, "trip-collection:"+trip+":"+driver).Scan(&n); e != nil || n != 1 {
		t.Fatal("one ledger entry", n, e)
	}
	if e = pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid`, other).Scan(&balance); e != nil || balance != 2500 {
		t.Fatal("debt beneficiary credit", balance, e)
	}
	var debtState string
	if e = pool.QueryRow(ctx, `SELECT status FROM rider_no_show_debts WHERE id=$1::uuid`, debt).Scan(&debtState); e != nil || debtState != "settled" {
		t.Fatal("debt settlement", debtState, e)
	}
	var state, method string
	if e = pool.QueryRow(ctx, `SELECT status,collection_method FROM trip_settlements WHERE trip_id=$1::uuid`, trip).Scan(&state, &method); e != nil || state != "confirmed" || method != "monnify" {
		t.Fatal("settlement", state, method, e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE type='trip.event.settlement.updated' AND data->>'tripId'=$1`, trip).Scan(&n); e != nil {
		t.Fatal(e)
	}
	// Missing webhooks are recovered even when the Rider never reopens the app.
	newFare, newTrip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, newFare, rider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed')`, newTrip, rider, driver, newFare)
	exec(`INSERT INTO trip_settlements(trip_id,rider_id,driver_id,status,expected_amount_kobo,fare_amount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,'pending',10000,10000)`, newTrip, rider, driver)
	newBody := fmt.Sprintf(`{"tripId":%q,"riderId":%q}`, newTrip, rider)
	if w := call("/internal/trip-payments/checkout", newBody, "test-internal"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	exec(`UPDATE rider_trip_payments SET updated_at=NOW()-INTERVAL '2 minutes' WHERE trip_id=$1::uuid`, newTrip)
	if e = ReconcileTripPaymentsOnce(ctx, pool, provider); e != nil {
		t.Fatal("payment reconciliation", e)
	}
	if e = pool.QueryRow(ctx, `SELECT status FROM rider_trip_payments WHERE trip_id=$1::uuid`, newTrip).Scan(&state); e != nil || state != "success" {
		t.Fatal("missing webhook recovery", state, e)
	}
}
