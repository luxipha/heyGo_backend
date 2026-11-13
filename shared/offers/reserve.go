package offers

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

var ErrUnavailable = errors.New("offer changed or capacity exhausted; request a fresh fare")

// The reservation SQL and unchanged-fare invariant have passed the isolated
// integration test. Called in the trip creation transaction.
func Reserve(ctx context.Context, tx pgx.Tx, trip, fare, rider string, scheduled bool) error {
	var q Preview
	e := tx.QueryRow(ctx, `SELECT discount_offer_id::text,coin_offer_id::text,discount_kobo,coins,expires_at FROM rider_offer_quotes WHERE fare_id=$1::uuid`, fare).Scan(&q.DiscountID, &q.CoinID, &q.Discount, &q.Coins, &q.Expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if q.DiscountID == nil && q.CoinID == nil {
		return nil
	}
	if !Enabled() || scheduled || !q.Expires.After(time.Now()) {
		return ErrUnavailable
	}
	var eligible bool
	if e = tx.QueryRow(ctx, `SELECT active AND 'rider'=ANY(roles) AND NOT EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id=$1::uuid) FROM users WHERE id=$1::uuid`, rider).Scan(&eligible); e != nil {
		return e
	}
	if !eligible {
		return ErrUnavailable
	}
	var gross, base int64
	var market, category string
	var airport bool
	e = tx.QueryRow(ctx, `SELECT ROUND(f.total_fare_minor)::bigint,f.gocoin_earn_coins,f.pricing_market_code,f.package_slug,
 EXISTS(SELECT 1 FROM trip_geofences g WHERE g.kind='airport' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND (ST_Covers(g.boundary,f.pickup::geometry) OR ST_Covers(g.boundary,f.destination::geometry)))
 FROM ride_fares f WHERE f.id=$1::uuid AND f.rider_id=$2::uuid`, fare, rider).Scan(&gross, &base, &market, &category, &airport)
	if e != nil {
		return e
	}
	ids := []string{}
	if q.DiscountID != nil {
		ids = append(ids, *q.DiscountID)
	}
	if q.CoinID != nil {
		ids = append(ids, *q.CoinID)
	}
	sort.Strings(ids)
	contains := func(values []string, value string) bool {
		for _, v := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	for _, id := range ids {
		r, e := Scan(tx.QueryRow(ctx, `SELECT `+Columns+` FROM rider_offers WHERE id=$1::uuid FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if !WithinWindow(r, time.Now()) || !contains(r.Markets, market) || !contains(r.Categories, category) || (r.AirportOnly && !airport) {
			return ErrUnavailable
		}
		var inZone bool
		if e = tx.QueryRow(ctx, `SELECT `+ZoneScopeSQL("$2")+` FROM rider_offers WHERE id=$1::uuid`, id, fare).Scan(&inZone); e != nil {
			return e
		}
		if !inZone {
			return ErrUnavailable
		}
		if r.Code != "" {
			var claimed bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rider_offer_claims WHERE offer_id=$1::uuid AND rider_id=$2::uuid)`, id, rider).Scan(&claimed); e != nil {
				return e
			}
			if !claimed {
				return ErrUnavailable
			}
		}
		cost := int64(0)
		if r.Kind == "discount" {
			cost = q.Discount
			if q.DiscountID == nil || id != *q.DiscountID || cost != min(gross, r.Discount) {
				return ErrUnavailable
			}
		}
		if r.Kind == "gocoin_multiplier" && (q.CoinID == nil || id != *q.CoinID || q.Coins != base*int64(r.Multiplier)) {
			return ErrUnavailable
		}
		ok, e := Capacity(ctx, tx, r, rider, cost)
		if e != nil {
			return e
		}
		if !ok {
			return ErrUnavailable
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rider_offer_uses(trip_id,offer_id,rider_id,discount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, trip, id, rider, cost); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE trips SET offer_discount_kobo=$2,gocoin_earn_coins=$3 WHERE id=$1::uuid`, trip, q.Discount, q.Coins)
	return e
}
