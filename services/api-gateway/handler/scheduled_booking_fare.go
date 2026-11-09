package handler

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"time"
)

// Read-only validation with a row lock, mirroring booking creation's checks.
func replacementScheduledFare(ctx context.Context, tx pgx.Tx, fare, owner, market string, at time.Time, p schedulePolicy) (region, destinationRegion string, seconds float64, err error) {
	err = tx.QueryRow(ctx, `SELECT
 (SELECT CASE WHEN count(*)=1 THEN min(code) END FROM trip_geofences g WHERE g.kind='region' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND ST_Covers(g.boundary,f.pickup::geometry)),
 (SELECT CASE WHEN count(*)=1 THEN min(code) END FROM trip_geofences g WHERE g.kind='region' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND ST_Covers(g.boundary,f.destination::geometry)),
 (f.route->'routes'->0->>'duration')::double precision
 FROM ride_fares f JOIN pricing_versions pv ON pv.id=f.pricing_version_id JOIN ride_categories cat ON cat.slug=f.package_slug
 WHERE f.id=$1::uuid AND f.rider_id=$2::uuid AND f.expires_at>NOW() AND f.consumed_at IS NULL AND NOT EXISTS(SELECT 1 FROM rider_offer_quotes oq WHERE oq.fare_id=f.id AND (oq.discount_offer_id IS NOT NULL OR oq.coin_offer_id IS NOT NULL))
 AND cat.status='active' AND f.package_slug=ANY($3::text[]) AND f.pricing_market_code=ANY($4::text[]) AND f.pricing_market_code=$5
 AND pv.status='approved' AND pv.effective_from<=$6 AND (pv.effective_until IS NULL OR pv.effective_until>$6)
 AND driver_market_at(f.pickup)=$5 AND driver_market_at(f.destination)=$5
 AND NOT EXISTS(SELECT 1 FROM rider_restrictions WHERE rider_id=$2::uuid AND status='active')
 AND NOT EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id=$2::uuid) FOR UPDATE OF f`, fare, owner, p.Categories, p.Markets, market, at).Scan(&region, &destinationRegion, &seconds)
	if err == nil && (math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 86400) {
		err = errors.New("invalid route duration")
	}
	return
}
