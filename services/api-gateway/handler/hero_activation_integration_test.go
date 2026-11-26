package handler

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	driverrepo "github.com/luxipha/heyGo_backend/services/driver-service/repo"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	driverpb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	"github.com/luxipha/heyGo_backend/shared/referrals"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHeroesActivationSplitToggleAndPayments(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	hero, other, driver, rider, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO heroes_accounts(id,email,name,referral_code)VALUES($1::uuid,$1||'@example.test','Stage Hero','STAGE-'||upper(substr($1::text,1,8))),($2::uuid,$2||'@example.test','Other Hero','OTHER-'||upper(substr($2::text,1,8)))`, hero, other)
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO users(id,roles,phone_e164,phone_verified_at)VALUES($1::uuid,'{}',$3,now()),($2::uuid,ARRAY['rider'],NULL,NULL)`, driver, rider, fmt.Sprintf("+23480%08d", uint64(time.Now().UnixNano())%100000000))
	exec(`INSERT INTO driver_profiles(driver_id,display_name,manual_verification_status,admin_status,admin_reviewed_at)VALUES($1::uuid,'Stage Driver','approved','approved',now()+interval '1 hour')`, driver)
	exec(`INSERT INTO driver_vehicles(driver_id,package_slug,review_status,approval_status,approved_at)VALUES($1::uuid,'sedan','approved','approved',now()+interval '1 hour')`, driver)
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at)VALUES($1::uuid,$2::uuid,$3,'approved','test',now()+interval '5 days')`, uuid.NewString(), driver, kind)
	}
	exec(`INSERT INTO driver_inspections(id,driver_id,status)VALUES($1::uuid,$2::uuid,'completed')`, uuid.NewString(), driver)

	settings, err := referrals.Read(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	settings.Enabled = true
	settings.Rules["hero_driver"] = referrals.Rule{TripTarget: 30, RewardKobo: 1700000, Stages: []referrals.Stage{{TripTarget: 10, RewardKobo: 700000, ApprovalMode: "manual"}, {TripTarget: 30, RewardKobo: 1000000, ApprovalMode: "manual"}}}
	transaction := func(f func(pgx.Tx) error) {
		t.Helper()
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = f(tx); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	save := func(early bool) {
		t.Helper()
		r := settings.Rules["hero_driver"]
		r.EarlyActivation = early
		settings.Rules["hero_driver"] = r
		transaction(func(tx pgx.Tx) error { var e error; settings, e = referrals.Save(ctx, tx, settings, staff); return e })
	}
	read := func(view string) []referrals.StageProgress {
		t.Helper()
		v, e := referrals.ListStagePage(ctx, pool, hero, 0, view)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	save(false)
	transaction(func(tx pgx.Tx) error {
		return referrals.Claim(ctx, tx, driver, "STAGE-"+strings.ToUpper(hero[:8]), "driver")
	})
	exec(`UPDATE users SET roles=ARRAY['driver'] WHERE id=$1::uuid`, driver)
	if v := read("current"); len(v) != 1 || v[0].RewardKobo != 700000 {
		t.Fatalf("combined current stage: %+v", v)
	}
	// Enabling applies to an existing referral without changing its original snapshot.
	save(true)
	all := read("all")
	if len(all) != 3 || all[0].Kind != "activation" || all[0].RewardKobo != 200000 || all[1].RewardKobo != 500000 {
		t.Fatalf("split: %+v", all)
	}
	activation, first := all[0].ID, all[1].ID
	if v := read("current"); len(v) != 1 || v[0].Kind != "activation" {
		t.Fatalf("activation should be sole current target: %+v", v)
	}
	if v := read("public"); len(v) != 2 || v[0].RewardKobo != 700000 || v[0].DriverID != "" || v[0].DriverPhone != "" {
		t.Fatalf("public compatibility: %+v", v)
	}
	save(false)
	if v := read("all"); len(v) != 2 || v[0].RewardKobo != 700000 {
		t.Fatalf("unearned merge: %+v", v)
	}
	save(true)
	if v := read("all"); len(v) != 3 || v[0].ID != activation {
		t.Fatalf("toggle must reuse activation: %+v", v)
	}
	// Approval alone, and a failed online attempt, do not earn activation.
	repo := driverrepo.NewPostgresDriverRepository(pool)
	if _, e := repo.UpsertOnline(ctx, &driverpb.Driver{Id: driver, PackageSlug: "sedan"}); e == nil {
		t.Fatal("online should fail without location")
	}
	if e := referrals.ProcessRewards(ctx, pool); e != nil {
		t.Fatal(e)
	}
	if v := read("all"); v[0].QualifiedAt != nil {
		t.Fatal("approval or failed online qualified activation")
	}
	// A real successful repository transition writes the durable event atomically.
	approver := uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, approver)
	market := "ACT_" + uuid.NewString()
	exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,'market',$2,$2,1,ST_GeomFromText('MULTIPOLYGON(((100 10,101 10,101 11,100 11,100 10)))',4326),'approved',now()-interval '1 day',$3::uuid,$4::uuid,now())`, uuid.NewString(), market, staff, approver)
	exec(`INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,1,0,0,false,0,'approved',now()-interval '1 day',$3::uuid,$4::uuid,now())`, uuid.NewString(), market, staff, approver)
	exec(`INSERT INTO driver_live_locations(driver_id,location)VALUES($1::uuid,ST_SetSRID(ST_MakePoint(100.5,10.5),4326)::geography)`, driver)
	if _, e := repo.UpsertOnline(ctx, &driverpb.Driver{Id: driver, PackageSlug: "sedan"}); e != nil {
		t.Fatal(e)
	}
	var online time.Time
	if e := pool.QueryRow(ctx, `SELECT first_online_at FROM driver_first_online WHERE driver_id=$1::uuid`, driver).Scan(&online); e != nil {
		t.Fatal(e)
	}
	if _, e := repo.UpsertOnline(ctx, &driverpb.Driver{Id: driver, PackageSlug: "sedan"}); e != nil {
		t.Fatal(e)
	}
	var again time.Time
	if e := pool.QueryRow(ctx, `SELECT first_online_at FROM driver_first_online WHERE driver_id=$1::uuid`, driver).Scan(&again); e != nil || !again.Equal(online) {
		t.Fatal("first online changed", e)
	}
	// Disabling before maintenance must preserve activation already earned.
	save(false)
	if v := read("all"); len(v) != 3 || v[0].QualifiedAt == nil || v[1].RewardKobo != 500000 {
		t.Fatalf("earned activation lost: %+v", v)
	}
	if v := read("current"); len(v) != 2 || v[0].Kind != "activation" || v[1].Position != 1 {
		t.Fatalf("earned unpaid plus next target: %+v", v)
	}
	transaction(func(tx pgx.Tx) error { return referrals.ApproveStages(ctx, tx, []string{activation}, staff) })
	transaction(func(tx pgx.Tx) error { return referrals.ApproveStages(ctx, tx, []string{activation}, staff) })
	var earned int64
	if e := pool.QueryRow(ctx, `SELECT sum(amount_kobo)FROM heroes_wallet_entries WHERE hero_id=$1::uuid`, hero).Scan(&earned); e != nil || earned != 200000 {
		t.Fatal("activation credit not idempotent", earned, e)
	}
	// Manual payment allocations use exactly the reserved activation amount.
	bank := uuid.NewString()
	exec(`INSERT INTO heroes_bank_accounts(id,hero_id,bank_name,account_name_ciphertext,account_number_ciphertext,account_number_masked)VALUES($1::uuid,$2::uuid,'Test bank',decode('00','hex'),decode('00','hex'),'******1234')`, bank, hero)
	payment := uuid.NewString()
	exec(`INSERT INTO heroes_manual_payments(id,hero_id,amount_kobo,reference,paid_at,recorded_by,bank_account_id)VALUES($1::uuid,$2::uuid,200000,$1,now(),$3::uuid,$4::uuid)`, payment, hero, staff, bank)
	transaction(func(tx pgx.Tx) error {
		return referrals.AllocateStagePayment(ctx, tx, hero, payment, 200000, []string{activation})
	})
	if v := read("current"); len(v) != 1 || v[0].Position != 1 {
		t.Fatalf("paid activation should leave work queue: %+v", v)
	}
	// Trip progress advances independently of payment and credit is only the remainder.
	fare := uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,completed_at) SELECT gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,'completed',now() FROM generate_series(1,10)`, rider, driver, fare)
	if e := referrals.ProcessRewards(ctx, pool); e != nil {
		t.Fatal(e)
	}
	transaction(func(tx pgx.Tx) error { return referrals.ApproveStages(ctx, tx, []string{first}, staff) })
	if e := pool.QueryRow(ctx, `SELECT sum(amount_kobo)FROM heroes_wallet_entries WHERE hero_id=$1::uuid`, hero).Scan(&earned); e != nil || earned != 700000 {
		t.Fatal("first total must be 7000", earned, e)
	}
	if v := read("current"); len(v) != 2 || v[0].Position != 1 || v[1].Position != 2 {
		t.Fatalf("unpaid first plus next target: %+v", v)
	}
	save(false)
	save(true)
	if v := read("all"); v[1].RewardKobo != 500000 {
		t.Fatal("paid allocation changed")
	}
	// Current filtering must happen before the 200-stage API page limit.
	exec(`WITH people AS (INSERT INTO users(id,roles) SELECT gen_random_uuid(),ARRAY['driver'] FROM generate_series(1,60) RETURNING id),
 refs AS (INSERT INTO referral_attributions(id,hero_id,invitee_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target)
 SELECT gen_random_uuid(),$1::uuid,id,'hero','driver',$2,1700000,30 FROM people RETURNING id)
 INSERT INTO hero_referral_stages(id,referral_id,position,trip_target,reward_kobo,approval_mode)
 SELECT gen_random_uuid(),id,n,n*10,700000,'manual' FROM refs CROSS JOIN generate_series(1,4)n`, hero, settings.Version)
	if v := read("current"); len(v) != 62 {
		t.Fatalf("current targets filtered after pagination: got %d want 62", len(v))
	}

	// A first trip reward already earned at the full amount must never be split.
	legacyDriver, legacyReferral, legacyStage := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver'])`, legacyDriver)
	exec(`INSERT INTO referral_attributions(id,hero_id,invitee_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target)VALUES($1::uuid,$2::uuid,$3::uuid,'hero','driver',$4,700000,10)`, legacyReferral, hero, legacyDriver, settings.Version)
	exec(`INSERT INTO hero_referral_stages(id,referral_id,position,trip_target,reward_kobo,approval_mode,qualified_at)VALUES($1::uuid,$2::uuid,1,10,700000,'manual',now())`, legacyStage, legacyReferral)
	save(true)
	var legacyAmount int64
	if e := pool.QueryRow(ctx, `SELECT hero_stage_amount($1::uuid)`, legacyStage).Scan(&legacyAmount); e != nil || legacyAmount != 700000 {
		t.Fatal("earned combined reward was split", legacyAmount, e)
	}
	// New referrals also capture the live early-payment setting.
	newDriver := uuid.NewString()
	exec(`INSERT INTO users(id,roles,phone_e164,phone_verified_at)VALUES($1::uuid,'{}',$2,now())`, newDriver, fmt.Sprintf("+23480%08d", uint64(time.Now().UnixNano())%100000000))
	transaction(func(tx pgx.Tx) error {
		return referrals.Claim(ctx, tx, newDriver, "STAGE-"+strings.ToUpper(hero[:8]), "driver")
	})
	var newActivation int
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE a.invitee_id=$1::uuid AND s.position=0 AND s.enabled`, newDriver).Scan(&newActivation); e != nil || newActivation != 1 {
		t.Fatal("new referral missing activation", newActivation, e)
	}

}
