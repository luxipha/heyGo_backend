package handler

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestNoShowDebtTransfersOnlyWhenTheCollectingDriverCanFundIt(t *testing.T) {
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
	rider, collector, claimant, trip, fare, claim, debt, policyID, admin1, admin2 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "NS_" + trip[:8]
	for _, u := range []struct{ id, role string }{{rider, "rider"}, {collector, "driver"}, {claimant, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "no-show:"+u.id, "human:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{admin1, admin2} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test-only')`, id, "no-show-admin-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,status,effective_from,created_by,approved_by,approved_at)
		VALUES($1::UUID,$2,1,2000,3000,FALSE,0,'approved',NOW()-INTERVAL '1 minute',$3::UUID,$4::UUID,NOW())`, uuid.NewString(), market, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{collector, claimant} {
		if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested,online_market_code) VALUES($1::UUID,'Driver','TEST','sedan','offline',FALSE,$2)`, id, market); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,500),($2::UUID,300000)`, claimant, collector); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at)
		VALUES($1::UUID,$2,1,5,100000,'approved',NOW()-INTERVAL '1 minute',$3::UUID,$4::UUID,NOW())`, policyID, market, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,no_show_debt_kobo) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB,100000)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,market_code,no_show_debt_kobo) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed',$5,100000)`, trip, rider, fare, collector, market); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_no_show_claims(id,trip_id,driver_id,rider_id,market_code,policy_id,fee_kobo,arrived_at,eligible_at,status,reviewed_by,reviewed_at)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6::UUID,100000,NOW()-INTERVAL '10 minutes',NOW()-INTERVAL '5 minutes','approved',$7::UUID,NOW())`, claim, trip, claimant, rider, market, policyID, admin1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rider_no_show_debts(id,claim_id,rider_id,beneficiary_driver_id,amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,100000,'due')`, debt, claim, rider, claimant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_no_show_debt_settlements(trip_id,debt_id,beneficiary_driver_id,amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,100000,'pending')`, trip, debt, claimant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,fare_amount_kobo,no_show_debt_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,600000,500000,100000,'pending')`, trip, collector, rider); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	ctxHTTP, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctxHTTP.Request = httptest.NewRequest("POST", "/driver/trips/settlement/confirm", nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := settleNoShowDebtTransfers(ctxHTTP, tx, trip, collector); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("settle transfer: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var collectorBalance, claimantBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID`, collector).Scan(&collectorBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID`, claimant).Scan(&claimantBalance); err != nil {
		t.Fatal(err)
	}
	if collectorBalance != 200000 || claimantBalance != 100500 {
		t.Fatalf("Operating Balances collector=%d claimant=%d", collectorBalance, claimantBalance)
	}
	var debtStatus, claimStatus string
	if err := pool.QueryRow(ctx, `SELECT d.status,c.status FROM rider_no_show_debts d JOIN driver_no_show_claims c ON c.id=d.claim_id WHERE d.id=$1::UUID`, debt).Scan(&debtStatus, &claimStatus); err != nil {
		t.Fatal(err)
	}
	if debtStatus != "settled" || claimStatus != "settled" {
		t.Fatalf("debt=%s claim=%s", debtStatus, claimStatus)
	}
}
