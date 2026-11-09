package offers

import (
	"context"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/referrals"
	"os"
	"testing"
)

func TestOfferPreviewIsImmutableAndCapacity(t *testing.T) {
	dsn := os.Getenv("OFFERS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated offers database required")
	}
	t.Setenv("HEYGO_OFFERS_ENABLED", "true")
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
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
	rider, second, staff := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{rider, second} {
		exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider'])`, id)
	}
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO gocoin_policies(earning_enabled,categories)VALUES(true,'{"sedan":{"enabled":true,"coinsPerRide":10}}')`)
	offer, coin := uuid.NewString(), uuid.NewString()
	for _, id := range []string{offer, coin} {
		exec(`INSERT INTO admin_campaign_drafts(id,kind,name,category,audience,body,budget_kobo,created_by,updated_by)VALUES($1::uuid,'promotion','Test offer','Discount','Riders','Test offer',50000,$2::uuid,$2::uuid)`, id, staff)
	}
	exec(`INSERT INTO rider_offers(id,title,message,kind,discount_kobo,code,starts_at,ends_at,market_codes,category_slugs,budget_kobo,max_uses,per_rider_limit,created_by)VALUES($1::uuid,'Discount','Discount','discount',50000,'WELCOME500',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 day',ARRAY['LAGOS'],ARRAY['sedan'],50000,1,1,$2::uuid)`, offer, staff)
	exec(`INSERT INTO rider_offers(id,title,message,kind,multiplier,starts_at,ends_at,market_codes,category_slugs,max_uses,per_rider_limit,created_by)VALUES($1::uuid,'Double coins','Double coins','gocoin_multiplier',2,NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 day',ARRAY['LAGOS'],ARRAY['sedan'],10,2,$2::uuid)`, coin, staff)
	makeFare := func(owner string) string {
		id := uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,pricing_market_code)VALUES($1::uuid,$2::uuid,'sedan',100000,'{}',ST_SetSRID(ST_MakePoint(3.4,6.4),4326)::geography,ST_SetSRID(ST_MakePoint(3.5,6.5),4326)::geography,'LAGOS')`, id, owner)
		return id
	}
	fare := makeFare(rider)
	q, e := Quote(ctx, pool, fare, rider, false)
	if e != nil || q.Discount != 0 || q.Coins != 20 {
		t.Fatal("unclaimed private code", q, e)
	}
	exec(`INSERT INTO rider_offer_claims(offer_id,rider_id)VALUES($1::uuid,$2::uuid)`, offer, rider)
	again, e := Quote(ctx, pool, fare, rider, false)
	if e != nil || again.Discount != 0 {
		t.Fatal("existing proposal changed", again, e)
	}
	fare = makeFare(rider)
	q, e = Quote(ctx, pool, fare, rider, false)
	if e != nil || q.Discount != 50000 || q.Coins != 20 {
		t.Fatal("claimed offer", q, e)
	}
	var gross, base int64
	if e = pool.QueryRow(ctx, `SELECT ROUND(total_fare_minor)::bigint,gocoin_earn_coins FROM ride_fares WHERE id=$1::uuid`, fare).Scan(&gross, &base); e != nil || gross != 100000 || base != 10 {
		t.Fatal("preview mutated money or base coins", gross, base, e)
	}
	scheduled, e := Quote(ctx, pool, makeFare(rider), rider, true)
	if e != nil || scheduled.Discount != 0 || scheduled.Coins != 10 {
		t.Fatal("scheduled exclusions", scheduled, e)
	}
	if _, e = Quote(ctx, pool, fare, second, false); e == nil {
		t.Fatal("foreign fare quoted")
	}
	// A proposal only becomes a usage in the booking transaction. Different
	// Riders must never consume the same last budget slot concurrently.
	trip := uuid.NewString()
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,'pending')`, trip, rider, fare)
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = Reserve(ctx, tx, trip, fare, rider, false); e != nil {
		_ = tx.Rollback(ctx)
		t.Fatal("reserve", e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var promised int64
	if e = pool.QueryRow(ctx, `SELECT gocoin_earn_coins FROM trips WHERE id=$1::uuid`, trip).Scan(&promised); e != nil || promised != 20 {
		t.Fatal("reserved coins", promised, e)
	}
	empty, e := Quote(ctx, pool, makeFare(second), second, false)
	if e != nil || empty.Discount != 0 {
		t.Fatal("budget exceeded", empty, e)
	}
	tx, e = pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	r, e := Read(ctx, tx, offer)
	if e != nil {
		t.Fatal(e)
	}
	ok, e := Capacity(ctx, tx, r, rider, 50000)
	_ = tx.Rollback(ctx)
	if e != nil || ok {
		t.Fatal("usage limit not enforced", e)
	}
	// Explicit cancellation releases reservations; no wallet was credited.
	exec(`UPDATE trips SET status='cancelled' WHERE id=$1::uuid`, trip)
	var reserved int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM rider_offer_uses WHERE trip_id=$1::uuid AND status='reserved'`, trip).Scan(&reserved); e != nil || reserved != 0 {
		t.Fatal("cancelled budget not released", e)
	}
	var balance int64
	if e = pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid),0)`, rider).Scan(&balance); e != nil || balance != 0 {
		t.Fatal("cancelled offer created money", balance, e)
	}
	exec(`INSERT INTO rider_offer_claims(offer_id,rider_id)VALUES($1::uuid,$2::uuid)`, offer, second)
	type candidate struct{ trip, fare, owner string }
	candidates := []candidate{{uuid.NewString(), makeFare(rider), rider}, {uuid.NewString(), makeFare(second), second}}
	for _, c := range candidates {
		if p, e := Quote(ctx, pool, c.fare, c.owner, false); e != nil || p.Discount != 50000 {
			t.Fatal("concurrent proposal", p, e)
		}
	}
	type outcome struct {
		c   candidate
		err error
	}
	results := make(chan outcome, 2)
	for _, c := range candidates {
		go func(c candidate) {
			tx, e := pool.Begin(ctx)
			if e == nil {
				_, e = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, c.owner)
				if e == nil {
					_, e = tx.Exec(ctx, `SELECT id FROM ride_fares WHERE id=$1::uuid FOR UPDATE`, c.fare)
				}
				if e == nil {
					_, e = tx.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,'pending')`, c.trip, c.owner, c.fare)
				}
				if e == nil {
					e = Reserve(ctx, tx, c.trip, c.fare, c.owner, false)
				}
				if e == nil {
					e = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
				}
			}
			results <- outcome{c, e}
		}(c)
	}
	wins := 0
	var winner candidate
	for i := 0; i < 2; i++ {
		o := <-results
		if o.err == nil {
			wins++
			winner = o.c
		} else if o.err != ErrUnavailable {
			t.Fatal(o.err)
		}
	}
	if wins != 1 {
		t.Fatal("budget oversubscribed", wins)
	}
	var committed int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM trips WHERE id IN($1::uuid,$2::uuid)`, candidates[0].trip, candidates[1].trip).Scan(&committed); e != nil || committed != 1 {
		t.Fatal("failed reservation did not roll back trip", committed, e)
	}
	// Verify the existing settlement path reimburses the Driver in full while
	// the promotional credit covers part of the gross fare exactly once.
	driver := uuid.NewString()
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver'])`, driver)
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status)VALUES($1::uuid,'Test Driver','TEST','sedan','available')`, driver)
	exec(`UPDATE trips SET assigned_driver_id=$2::uuid,status='completed',completed_at=NOW() WHERE id=$1::uuid`, winner.trip, driver)
	tx, e = pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	for i := 0; i < 2; i++ {
		if e = Complete(ctx, tx, winner.trip); e != nil {
			t.Fatal("promotion completion", e)
		}
	}
	spent, e := referrals.SpendRideCredit(ctx, tx, winner.trip)
	if e != nil || spent != 50000 {
		t.Fatal("discount settlement", spent, e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,fare_amount_kobo,status,ride_credit_kobo,resolved_at)VALUES($1::uuid,$2::uuid,$3::uuid,50000,100000,'confirmed',50000,NOW())`, winner.trip, driver, winner.owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var driverCredit, coinsEarned int64
	if e = pool.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid`, driver).Scan(&driverCredit); e != nil || driverCredit != 50000 {
		t.Fatal("Driver subsidy", driverCredit, e)
	}
	if e = pool.QueryRow(ctx, `SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid`, winner.owner).Scan(&coinsEarned); e != nil || coinsEarned != 20 {
		t.Fatal("multiplied reward", coinsEarned, e)
	}
}
