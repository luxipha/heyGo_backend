package offers

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Preview struct {
	DiscountID *string   `json:"discountOfferId"`
	CoinID     *string   `json:"coinOfferId"`
	Discount   int64     `json:"offerDiscountKobo"`
	Coins      int64     `json:"goCoinsOnCompletion"`
	Expires    time.Time `json:"expiresAt"`
}

// Previewing never changes a fare or awards/spends anything. Immutable offer
// proposals are checked and reserved atomically during trip creation.
func Quote(ctx context.Context, pool *pgxpool.Pool, fare, rider string, scheduled bool) (out Preview, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var amount, base int64
	var market, category string
	var airport bool
	err = tx.QueryRow(ctx, `SELECT ROUND(f.total_fare_minor)::bigint,f.gocoin_earn_coins,COALESCE(f.pricing_market_code,''),f.package_slug,f.expires_at,
 EXISTS(SELECT 1 FROM trip_geofences g WHERE g.kind='airport' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND (ST_Covers(g.boundary,f.pickup::geometry) OR ST_Covers(g.boundary,f.destination::geometry)))
 FROM ride_fares f WHERE f.id=$1::uuid AND f.rider_id=$2::uuid AND f.consumed_at IS NULL`, fare, rider).Scan(&amount, &base, &market, &category, &out.Expires, &airport)
	if err != nil {
		return out, err
	}
	out.Coins = base
	if !Enabled() || scheduled {
		return out, nil
	}
	err = tx.QueryRow(ctx, `SELECT discount_offer_id::text,coin_offer_id::text,discount_kobo,coins,expires_at FROM rider_offer_quotes WHERE fare_id=$1::uuid`, fare).Scan(&out.DiscountID, &out.CoinID, &out.Discount, &out.Coins, &out.Expires)
	if err == nil {
		return out, nil
	}
	if err != pgx.ErrNoRows {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+Columns+` FROM rider_offers WHERE active AND starts_at<=NOW() AND ends_at>NOW() AND $1=ANY(market_codes) AND $2=ANY(category_slugs) AND (NOT airport_only OR $3) AND `+ZoneScopeSQL("$5")+` AND (code IS NULL OR EXISTS(SELECT 1 FROM rider_offer_claims c WHERE c.offer_id=rider_offers.id AND c.rider_id=$4::uuid)) ORDER BY id`, market, category, airport, rider, fare)
	if err != nil {
		return out, err
	}
	rules := []Rule{}
	for rows.Next() {
		r, e := Scan(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		rules = append(rules, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	var dr, cr *Rule
	for i := range rules {
		r := rules[i]
		if !WithinWindow(r, now) {
			continue
		}
		value := min(r.Discount, amount)
		ok, e := Capacity(ctx, tx, r, rider, value)
		if e != nil {
			return out, e
		}
		if !ok {
			continue
		}
		if r.Kind == "discount" && value > out.Discount {
			out.Discount = value
			id := r.ID
			out.DiscountID = &id
			copy := r
			dr = &copy
		}
		if r.Kind == "gocoin_multiplier" && base > 0 && base*int64(r.Multiplier) > out.Coins {
			out.Coins = base * int64(r.Multiplier)
			id := r.ID
			out.CoinID = &id
			copy := r
			cr = &copy
		}
	}
	for _, r := range []*Rule{dr, cr} {
		if r == nil {
			continue
		}
		loc, _ := time.LoadLocation(r.Timezone)
		local := now.In(loc)
		end := time.Date(local.Year(), local.Month(), local.Day(), r.EndHour, 0, 0, 0, loc).UTC()
		if r.Ends.Before(end) {
			end = r.Ends
		}
		if end.Before(out.Expires) {
			out.Expires = end
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO rider_offer_quotes(fare_id,discount_offer_id,coin_offer_id,discount_kobo,coins,expires_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6) ON CONFLICT DO NOTHING`, fare, out.DiscountID, out.CoinID, out.Discount, out.Coins, out.Expires)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT discount_offer_id::text,coin_offer_id::text,discount_kobo,coins,expires_at FROM rider_offer_quotes WHERE fare_id=$1::uuid`, fare).Scan(&out.DiscountID, &out.CoinID, &out.Discount, &out.Coins, &out.Expires)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return
}
