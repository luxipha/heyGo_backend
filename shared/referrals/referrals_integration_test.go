package referrals

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"os"
	"sync"
	"testing"
	"time"
)

func TestProgrammeRulesAndAtomicApprovals(t *testing.T) {
	url := os.Getenv("REFERRAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("Disposable referral database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, url)
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
	staff, driver, newUser, existing, otherRider := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver']),($2::uuid,'{}'),($3::uuid,ARRAY['rider']),($4::uuid,ARRAY['rider'])`, driver, newUser, existing, otherRider)
	exec(`UPDATE users SET referral_signup_open=FALSE WHERE id=$1::uuid`, existing)
	code, e := OwnCode(ctx, pool, driver, "driver")
	if e != nil {
		t.Fatal(e)
	}
	// The suite may be rerun against the same disposable database.
	initial, err := Read(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	initial.Enabled = false
	setupTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Save(ctx, setupTx, initial, staff); err != nil {
		setupTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claim := func(user string) error {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = Claim(ctx, tx, user, code, "rider"); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if e = claim(newUser); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("disabled claim: %v", e)
	}
	s, e := Read(ctx, pool)
	if e != nil {
		t.Fatal(e)
	}
	s.Enabled = true
	rule := s.Rules["driver_rider"]
	rule.RewardKobo = 50000
	rule.TripTarget = 1
	s.Rules["driver_rider"] = rule
	tx, _ := pool.Begin(ctx)
	s, e = Save(ctx, tx, s, staff)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = claim(existing); !errors.Is(e, ErrIneligible) {
		t.Fatalf("existing claim allowed: %v", e)
	}
	if e = claim(newUser); e != nil {
		t.Fatal(e)
	}
	if e = claim(newUser); e != nil {
		t.Fatal("idempotent claim", e)
	}
	exec(`UPDATE users SET roles=ARRAY['rider'],referral_signup_open=FALSE WHERE id=$1::uuid`, newUser)
	var id string
	if e = pool.QueryRow(ctx, `SELECT id::text FROM referral_attributions WHERE invitee_id=$1::uuid`, newUser).Scan(&id); e != nil {
		t.Fatal(e)
	}
	// An approval cannot credit an unqualified referral.
	tx, _ = pool.Begin(ctx)
	if e = Approve(ctx, tx, []string{id}, staff); e == nil {
		t.Fatal("unqualified reward approved")
	}
	tx.Rollback(ctx)
	fare, trip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, newUser)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,completed_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed',now())`, trip, newUser, driver, fare)
	if e = ProcessRewards(ctx, pool); e != nil {
		t.Fatal(e)
	}
	var notification int
	pool.QueryRow(ctx, `SELECT count(*) FROM referral_admin_notifications WHERE referral_id=$1::uuid`, id).Scan(&notification)
	if notification != 1 {
		t.Fatal("missing admin notification")
	}
	// Existing statutory arrears are repaid by a reward credit just as by a top-up.
	charge, chargeRule := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO statutory_charge_rules(id,ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,calculation_config,bearer,funding_source,status,effective_from,created_by)VALUES($1::uuid,$1,1,'Test charge','Test','NG','fixed','fare','{"amountKobo":10000}','driver','operating_balance','draft',now(),$2::uuid)`, chargeRule, staff)
	exec(`INSERT INTO trip_statutory_charges(id,trip_id,rule_id,driver_id,amount_kobo,bearer,funding_source,rule_snapshot)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,10000,'driver','operating_balance','{}')`, charge, trip, chargeRule, driver)
	// Changing live policy never changes the amount already captured.
	s.Rules["driver_rider"] = Rule{RewardKobo: 99900, TripTarget: 10}
	tx, _ = pool.Begin(ctx)
	if _, e = Save(ctx, tx, s, staff); e != nil {
		t.Fatal(e)
	}
	tx.Commit(ctx)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, e := pool.Begin(ctx)
			if e != nil {
				errs <- e
				return
			}
			defer tx.Rollback(ctx)
			if e = Approve(ctx, tx, []string{id}, staff); e == nil {
				e = tx.Commit(ctx)
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	var balance int64
	pool.QueryRow(ctx, `SELECT count(*) FROM driver_operating_entries WHERE source_key=$1`, "referral:"+id).Scan(&count)
	pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid`, driver).Scan(&balance)
	if count != 1 || balance != 40000 {
		t.Fatalf("duplicate or wrong credit: %d %d", count, balance)
	}

	// Rider rewards are non-withdrawable credits; spending them reimburses the
	// assigned Driver atomically, and retrying the same trip never spends twice.
	riderReward, invitee := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider'])`, invitee)
	exec(`INSERT INTO referral_attributions(id,invitee_id,inviter_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target,qualified_at)VALUES($1::uuid,$2::uuid,$3::uuid,'rider','rider',$4,150000,1,now())`, riderReward, invitee, otherRider, s.Version)
	tx, _ = pool.Begin(ctx)
	if e = Approve(ctx, tx, []string{riderReward}, staff); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	creditFare, creditTrip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',100000,'{}')`, creditFare, otherRider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,completed_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed',now())`, creditTrip, otherRider, driver, creditFare)
	for i := 0; i < 2; i++ {
		tx, _ = pool.Begin(ctx)
		used, err := SpendRideCredit(ctx, tx, creditTrip)
		if err != nil || used != 100000 {
			t.Fatalf("spend credit: %d %v", used, err)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	pool.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid`, otherRider).Scan(&balance)
	if balance != 50000 {
		t.Fatalf("rider credit spent twice: %d", balance)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM driver_operating_entries WHERE source_key=$1`, "ride-credit:"+creditTrip).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate reimbursement")
	}
	// New Hero rewards remain visible to the existing Heroes payout dashboard.
	hero, heroInvitee, heroReward := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO heroes_accounts(id,email,name,password_hash,referral_code)VALUES($1::uuid,$1||'@example.test','Hero','x',$1)`, hero)
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver'])`, heroInvitee)
	exec(`INSERT INTO referral_attributions(id,invitee_id,hero_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target,qualified_at)VALUES($1::uuid,$2::uuid,$3::uuid,'hero','driver',$4,50000,30,now())`, heroReward, heroInvitee, hero, s.Version)
	tx, _ = pool.Begin(ctx)
	if e = Approve(ctx, tx, []string{heroReward}, staff); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM heroes_wallet_entries w JOIN heroes_all_referrals r ON r.id=w.programme_referral_id WHERE r.hero_id=$1::uuid`, hero).Scan(&count)
	if count != 1 {
		t.Fatal("Hero reward not visible")
	}
	// Attribution cannot be edited after confirmation.
	if _, e = pool.Exec(ctx, `UPDATE referral_attributions SET trip_target=99 WHERE id=$1::uuid`, id); e == nil {
		t.Fatal("attribution mutated")
	}
	// Rider codes cannot invite a driver.
	riderCode, e := OwnCode(ctx, pool, otherRider, "rider")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Lookup(ctx, pool, riderCode, "driver"); !errors.Is(e, ErrInvalid) {
		t.Fatal("rider invited driver", e)
	}
}
func TestValidateSettings(t *testing.T) {
	s := Settings{DisabledNotice: "Paused", DriverHeadline: "Invite", RiderHeadline: "Invite", Rules: map[string]Rule{}}
	for _, key := range RuleKeys {
		s.Rules[key] = Rule{TripTarget: 1}
	}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.Rules["driver_driver"] = Rule{TripTarget: 0}
	if s.Validate() == nil {
		t.Fatal("invalid target allowed")
	}
}
