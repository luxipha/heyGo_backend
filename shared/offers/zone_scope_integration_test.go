package offers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestValidateZones(t *testing.T) {
	base := Rule{Kind: "discount", Discount: 1000, Multiplier: 1, Budget: 1000, Starts: time.Now(), Ends: time.Now().Add(time.Hour), Markets: []string{"LAGOS"}, Categories: []string{"sedan"}, EndHour: 24, MaxUses: 1, PerRider: 1, Timezone: "Africa/Lagos"}
	if err := Validate(base); err != nil {
		t.Fatal("no zones is the existing market-wide behaviour", err)
	}
	one := uuid.NewString()
	for name, zones := range map[string][]string{
		"not a uuid": {"ikeja"},
		"duplicate":  {one, one},
		"too many": func() []string {
			z := []string{}
			for i := 0; i <= MaxZones; i++ {
				z = append(z, uuid.NewString())
			}
			return z
		}(),
	} {
		r := base
		r.Zones = zones
		if Validate(r) == nil {
			t.Fatal("accepted", name)
		}
	}
	r := base
	r.Zones = []string{one, uuid.NewString()}
	if err := Validate(r); err != nil {
		t.Fatal("valid zones rejected", err)
	}
}

func TestZoneScopedOffers(t *testing.T) {
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
	suffix := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:8]
	market := "ZONEMKT" + suffix
	creator, approver := uuid.NewString(), uuid.NewString()
	for _, id := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, id)
	}
	var created []string
	// Approved geofences cannot be deleted; retire them (and drop drafts) at the end.
	// A defer, not t.Cleanup: it must run before the pool is closed.
	defer func() {
		_, _ = pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=NOW() WHERE id=ANY($1::text[]::uuid[]) AND status='approved' AND effective_until IS NULL`, created)
		_, _ = pool.Exec(context.Background(), `DELETE FROM trip_geofences WHERE id=ANY($1::text[]::uuid[]) AND status='draft'`, created)
	}()
	geofence := func(kind, code, status, wkt, from, until string) string {
		id := uuid.NewString()
		created = append(created, id)
		approvedBy := any(approver)
		approvedAt := any(time.Now())
		if status == "draft" {
			approvedBy, approvedAt = nil, nil
		}
		var u any
		if until != "" {
			u = until
		}
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,effective_until,created_by,approved_by,approved_at)
 VALUES($1::uuid,$2,$3,$3,1,ST_Multi(ST_GeomFromText($4,4326)),$5,NOW()-$6::interval,CASE WHEN $7::text IS NULL THEN NULL ELSE NOW()-$7::interval END,$8::uuid,$9::uuid,$10)`, id, kind, code, wkt, status, from, u, creator, approvedBy, approvedAt)
		return id
	}
	// The market covers x 3.0-4.0, y 6.0-7.0. "Ikeja" holds the test pickup
	// (3.4, 6.4); "Surulere" is a different part of the same market.
	geofence("market", market, "approved", "POLYGON((3 6,4 6,4 7,3 7,3 6))", "2 days", "")
	ikeja := geofence("region", "IKEJA"+suffix, "approved", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))", "2 days", "")
	surulere := geofence("region", "SURULERE"+suffix, "approved", "POLYGON((3.6 6.4,3.8 6.4,3.8 6.6,3.6 6.6,3.6 6.4))", "2 days", "")
	draftZone := geofence("region", "DRAFT"+suffix, "draft", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))", "2 days", "")
	retired := geofence("region", "RETIRED"+suffix, "approved", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))", "2 days", "1 hour")
	outside := geofence("region", "OUTSIDE"+suffix, "approved", "POLYGON((9 9,9.5 9,9.5 9.5,9 9.5,9 9))", "2 days", "")
	marketOnly := geofence("market", market+"B", "approved", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))", "2 days", "")

	exec(`INSERT INTO gocoin_policies(earning_enabled,categories)VALUES(true,'{"sedan":{"enabled":true,"coinsPerRide":10}}')`)
	rider, second := uuid.NewString(), uuid.NewString()
	for _, id := range []string{rider, second} {
		exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider'])`, id)
	}
	newOffer := func(maxUses int, zones ...string) string {
		id := uuid.NewString()
		exec(`INSERT INTO admin_campaign_drafts(id,kind,name,category,audience,body,budget_kobo,created_by,updated_by)VALUES($1::uuid,'promotion','Zone offer','Discount','Riders','Zone offer',500000,$2::uuid,$2::uuid)`, id, creator)
		if zones == nil {
			zones = []string{}
		}
		exec(`INSERT INTO rider_offers(id,title,message,kind,discount_kobo,starts_at,ends_at,market_codes,category_slugs,budget_kobo,max_uses,per_rider_limit,created_by,zone_ids)VALUES($1::uuid,'Zone','Zone','discount',50000,NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 day',ARRAY[$2],ARRAY['sedan'],500000,$3,5,$4::uuid,$5::text[]::uuid[])`, id, market, maxUses, creator, zones)
		return id
	}
	fareAt := func(owner string, lon, lat float64) string {
		id := uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,pricing_market_code)VALUES($1::uuid,$2::uuid,'sedan',100000,'{}',ST_SetSRID(ST_MakePoint($3,$4),4326)::geography,ST_SetSRID(ST_MakePoint(3.7,6.5),4326)::geography,$5)`, id, owner, lon, lat, market)
		return id
	}
	discountAt := func(owner string, lon, lat float64) (int64, string) {
		t.Helper()
		q, e := Quote(ctx, pool, fareAt(owner, lon, lat), owner, false)
		if e != nil {
			t.Fatal(e)
		}
		id := ""
		if q.DiscountID != nil {
			id = *q.DiscountID
		}
		return q.Discount, id
	}
	deactivate := func(ids ...string) {
		for _, id := range ids {
			exec(`UPDATE rider_offers SET active=false WHERE id=$1::uuid`, id)
		}
	}

	t.Run("market-wide offer is unchanged", func(t *testing.T) {
		id := newOffer(100)
		defer deactivate(id)
		if d, _ := discountAt(rider, 3.4, 6.4); d != 50000 {
			t.Fatal("market-wide offer stopped applying", d)
		}
		if d, _ := discountAt(rider, 3.7, 6.5); d != 50000 {
			t.Fatal("market-wide offer stopped applying outside any zone", d)
		}
	})
	t.Run("applies inside the zone and not outside it", func(t *testing.T) {
		id := newOffer(100, ikeja)
		defer deactivate(id)
		if d, got := discountAt(rider, 3.4, 6.4); d != 50000 || got != id {
			t.Fatal("Ikeja pickup did not get the Ikeja offer", d)
		}
		if d, _ := discountAt(rider, 3.7, 6.5); d != 0 {
			t.Fatal("Surulere pickup got the Ikeja offer", d)
		}
		if d, _ := discountAt(rider, 3.9, 6.9); d != 0 {
			t.Fatal("pickup outside every zone got the Ikeja offer", d)
		}
	})
	t.Run("any of several zones matches", func(t *testing.T) {
		id := newOffer(100, surulere, ikeja)
		defer deactivate(id)
		if d, _ := discountAt(rider, 3.4, 6.4); d != 50000 {
			t.Fatal("first zone did not match", d)
		}
		if d, _ := discountAt(rider, 3.7, 6.5); d != 50000 {
			t.Fatal("second zone did not match", d)
		}
	})
	t.Run("draft, retired and market zones never match", func(t *testing.T) {
		for name, zone := range map[string]string{"draft": draftZone, "retired": retired, "market": marketOnly} {
			id := newOffer(100, zone)
			if d, _ := discountAt(rider, 3.4, 6.4); d != 0 {
				t.Fatal(name, "zone matched", d)
			}
			deactivate(id)
		}
	})
	t.Run("reservation rechecks the zone", func(t *testing.T) {
		id := newOffer(100, ikeja)
		defer deactivate(id)
		fare := fareAt(rider, 3.4, 6.4)
		if q, e := Quote(ctx, pool, fare, rider, false); e != nil || q.Discount != 50000 {
			t.Fatal("quote", q, e)
		}
		// The offer is re-scoped to another zone between quote and booking.
		exec(`UPDATE rider_offers SET zone_ids=ARRAY[$2::uuid] WHERE id=$1::uuid`, id, surulere)
		trip := uuid.NewString()
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,'pending')`, trip, rider, fare)
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = Reserve(ctx, tx, trip, fare, rider, false); e != ErrUnavailable {
			t.Fatal("reserved an offer whose zone no longer matches", e)
		}
	})
	t.Run("capacity still binds a zone offer", func(t *testing.T) {
		id := newOffer(1, ikeja)
		defer deactivate(id)
		fare := fareAt(rider, 3.4, 6.4)
		if q, e := Quote(ctx, pool, fare, rider, false); e != nil || q.Discount != 50000 {
			t.Fatal("quote", q, e)
		}
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
		if d, _ := discountAt(second, 3.4, 6.4); d != 0 {
			t.Fatal("zone offer exceeded its usage limit", d)
		}
	})
	t.Run("publish validation", func(t *testing.T) {
		ends := time.Now().Add(24 * time.Hour)
		check := func(name string, want bool, until time.Time, markets []string, zones ...string) {
			t.Helper()
			ok, e := ApprovedZones(ctx, pool, zones, markets, until)
			if e != nil || ok != want {
				t.Fatal(name, "got", ok, e)
			}
		}
		check("no zones", true, ends, []string{market})
		check("approved region inside the market", true, ends, []string{market}, ikeja, surulere)
		check("draft region", false, ends, []string{market}, draftZone)
		check("market kind", false, ends, []string{market}, marketOnly)
		check("zone outside the market", false, ends, []string{market}, outside)
		check("retired zone", false, ends, []string{market}, retired)
		check("unknown zone", false, ends, []string{market}, uuid.NewString())
		check("wrong market", false, ends, []string{"NOWHERE"}, ikeja)
		check("one bad zone spoils the set", false, ends, []string{market}, ikeja, draftZone)
		exec(`UPDATE trip_geofences SET effective_until=NOW()+INTERVAL '2 hours' WHERE id=$1::uuid`, surulere)
		check("zone ends before the offer does", false, ends, []string{market}, surulere)
		check("zone outlasts the offer", true, time.Now().Add(time.Hour), []string{market}, surulere)
	})
}
