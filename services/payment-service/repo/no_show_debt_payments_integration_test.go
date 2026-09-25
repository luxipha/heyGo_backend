package repo

import (
	"context"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/google/uuid"
)

func TestPostVerifiedNoShowDebtPaymentCreditsClaimantOnce(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, driver, trip, fare, claim, debt, policy, admin1, admin2 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	paymentID, paymentReference, transactionReference := uuid.NewString(), "test-debt-"+uuid.NewString(), "test-txn-"+uuid.NewString()
	market := "NSPAY_" + trip[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider']),($4::UUID,$5,$6,ARRAY['driver'])`, rider, "ns-pay:"+rider, "human:"+rider, driver, "ns-pay:"+driver, "human:"+driver); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{admin1, admin2} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test-only')`, id, "ns-pay-admin-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested,online_market_code) VALUES($1::UUID,'Driver','TEST','sedan','offline',FALSE,$2)`, driver, market); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,5,100000,'approved',NOW()-INTERVAL '1 minute',$3::UUID,$4::UUID,NOW())`, policy, market, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,market_code) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'cancelled',$5)`, trip, rider, fare, driver, market); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_no_show_claims(id,trip_id,driver_id,rider_id,market_code,policy_id,fee_kobo,arrived_at,eligible_at,status,reviewed_by,reviewed_at) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6::UUID,100000,NOW()-INTERVAL '10 minutes',NOW()-INTERVAL '5 minutes','approved',$7::UUID,NOW())`, claim, trip, driver, rider, market, policy, admin1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rider_no_show_debts(id,claim_id,rider_id,beneficiary_driver_id,amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,100000,'due')`, debt, claim, rider, driver); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rider_no_show_debt_payments(id,debt_id,rider_id,amount_kobo,provider,provider_reference,provider_transaction_reference,idempotency_key,status) VALUES($1::UUID,$2::UUID,$3::UUID,100000,'monnify',$4,$5,'test-key','pending')`, paymentID, debt, rider, paymentReference, transactionReference); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := PostVerifiedNoShowDebtPayment(ctx, pool, paymentReference, transactionReference, 100000); err != nil {
			t.Fatal(err)
		}
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID`, driver).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100000 {
		t.Fatalf("claimant balance=%d, want 100000", balance)
	}
	var debtStatus, claimStatus string
	if err := pool.QueryRow(ctx, `SELECT d.status,c.status FROM rider_no_show_debts d JOIN driver_no_show_claims c ON c.id=d.claim_id WHERE d.id=$1::UUID`, debt).Scan(&debtStatus, &claimStatus); err != nil {
		t.Fatal(err)
	}
	if debtStatus != "settled" || claimStatus != "settled" {
		t.Fatalf("debt=%s claim=%s", debtStatus, claimStatus)
	}
	var entries, events int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_operating_entries WHERE source_key=$1`, "no-show-debt-payment:"+transactionReference).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id IN ($1::UUID,$2::UUID) AND type='trip.event.no_show_claim_updated'`, driver, rider).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || events != 2 {
		t.Fatalf("entries=%d events=%d, want 1 and 2", entries, events)
	}
}
