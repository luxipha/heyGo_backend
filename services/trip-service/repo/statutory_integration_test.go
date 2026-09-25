package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	paymentrepo "github.com/luxipha/heyGo_backend/services/payment-service/repo"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/google/uuid"
)

func TestTripCompletionAssessesChargeAndTopupRepaysArrears(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	driver, rider, trip, fare, admin1, admin2, rule, policy := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "TEST_" + suffix[:8]
	tag := "TEST_" + suffix[9:17]
	for _, u := range []struct{ id, role string }{{driver, "driver"}, {rider, "rider"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4]::TEXT[])`, u.id, "cid-"+u.id, "hid-"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{admin1, admin2} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, id, fmt.Sprintf("test-%s-%d", suffix, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,available,online_requested,online_market_code) VALUES($1::UUID,'Test','TST-1','sedan','on_trip',FALSE,FALSE,$2)`, driver, market); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,1000)`, driver); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,topup_minimum_kobo,topup_maximum_kobo,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,2000,3000,FALSE,0,100,100000,'approved',NOW()-INTERVAL '1 minute',$3::UUID,$4::UUID,NOW())`, policy, market, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	config := json.RawMessage(`{"amountNaira":20}`)
	if _, err := pool.Exec(ctx, `INSERT INTO statutory_charge_rules(id,ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,calculation_config,market_code,regulatory_tag,bearer,funding_source,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,'Test charge','Test authority','Test market','fixed','trip_fare',$3::JSONB,$4,$5,'driver','operating_balance','approved',NOW()-INTERVAL '1 minute',$6::UUID,$7::UUID,NOW())`, rule, "TST_"+suffix[:8], config, market, tag, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}'::JSONB)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,started_at,market_code,regulatory_tags) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'started',NOW(),$5,ARRAY[$6]::TEXT[])`, trip, rider, fare, driver, market, tag); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool)
	if _, changed, err := repo.Complete(ctx, trip, driver); err != nil || !changed {
		t.Fatalf("complete: changed=%v err=%v", changed, err)
	}
	if _, changed, err := repo.Complete(ctx, trip, driver); err != nil || changed {
		t.Fatalf("replay complete: changed=%v err=%v", changed, err)
	}
	var balance, assessed, paid int64
	if err := pool.QueryRow(ctx, `SELECT a.balance_kobo,c.amount_kobo,COALESCE(SUM(r.amount_kobo),0)::BIGINT FROM driver_operating_accounts a JOIN trip_statutory_charges c ON c.driver_id=a.driver_id LEFT JOIN statutory_charge_repayments r ON r.charge_id=c.id WHERE a.driver_id=$1::UUID GROUP BY a.balance_kobo,c.amount_kobo`, driver).Scan(&balance, &assessed, &paid); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || assessed != 2000 || paid != 1000 {
		t.Fatalf("post-trip state balance=%d assessed=%d paid=%d", balance, assessed, paid)
	}
	var notifications int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID AND type='balance'`, driver).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("statutory charge notifications=%d err=%v", notifications, err)
	}
	var eligible bool
	if err := pool.QueryRow(ctx, `SELECT driver_balance_eligible($1::UUID,$2)`, driver, market).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible {
		t.Fatal("driver eligible with an outstanding statutory charge")
	}
	topupID := uuid.NewString()
	ref := "TEST-" + suffix
	trans := "MNFY-TEST-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_topups(id,driver_id,amount_kobo,provider,provider_reference,status) VALUES($1::UUID,$2::UUID,3000,'monnify',$3,'pending')`, topupID, driver, ref); err != nil {
		t.Fatal(err)
	}
	posted, err := paymentrepo.PostVerifiedOperatingTopup(ctx, pool, ref, trans, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if posted.BalanceAfterKobo != 2000 {
		t.Fatalf("post-repayment balance=%d, want 2000", posted.BalanceAfterKobo)
	}
	if err := pool.QueryRow(ctx, `SELECT driver_balance_eligible($1::UUID,$2)`, driver, market).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if !eligible {
		t.Fatal("driver remains ineligible after arrears are paid and minimum is met")
	}
	var arrears int64
	if err := pool.QueryRow(ctx, `SELECT c.amount_kobo-COALESCE(SUM(r.amount_kobo),0) FROM trip_statutory_charges c LEFT JOIN statutory_charge_repayments r ON r.charge_id=c.id WHERE c.driver_id=$1::UUID GROUP BY c.id`, driver).Scan(&arrears); err != nil || arrears != 0 {
		t.Fatalf("arrears=%d err=%v", arrears, err)
	}
}
