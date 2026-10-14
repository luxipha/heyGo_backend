package driverstate

import "fmt"

const MatchingRadiusMeters = 15000

// AvailabilityPredicate is shared by dispatch and Rider availability. Callers
// join driver_eligibility e and driver_live_locations l using driver alias d.
// Market/package/point/radius are trusted SQL expressions, never user text.
func AvailabilityPredicate(market, packageSlug, point, radius string) string {
	return fmt.Sprintf(`d.package_slug=%s AND d.status='available' AND d.available=TRUE
 AND d.online_requested=TRUE AND d.location IS NOT NULL AND e.eligible_to_drive=TRUE
 AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>NOW())
 AND d.online_market_code=%s AND driver_market_at(d.location)=%s
 AND l.recorded_at>=NOW()-INTERVAL '30 minutes'
 AND driver_balance_eligible(d.id,d.online_market_code)
 AND ST_DWithin(d.location,%s,%s)`, packageSlug, market, market, point, radius)
}
