package offers

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"os"
	"regexp"
	"strings"
	"time"
)

type Rule struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	Kind        string    `json:"kind"`
	Discount    int64     `json:"discountKobo"`
	Multiplier  int       `json:"multiplier"`
	Code        string    `json:"code,omitempty"`
	Starts      time.Time `json:"startsAt"`
	Ends        time.Time `json:"endsAt"`
	Markets     []string  `json:"markets"`
	Categories  []string  `json:"categories"`
	AirportOnly bool      `json:"airportOnly"`
	// Zones limits the offer to fares picked up inside these approved
	// region/airport geofences. Empty means the whole market.
	Zones     []string `json:"zoneIds"`
	StartHour int      `json:"startHour"`
	EndHour   int      `json:"endHour"`
	Timezone  string   `json:"timezone"`
	Budget    int64    `json:"budgetKobo"`
	MaxUses   int      `json:"maxUses"`
	PerRider  int      `json:"perRiderLimit"`
	Active    bool     `json:"active"`
}

var codePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{3,31}$`)

// Disabled by default; publishing and fare application both require opt-in.
func Enabled() bool                    { return os.Getenv("HEYGO_OFFERS_ENABLED") == "true" }
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }
func ValidCode(code string) bool       { return codePattern.MatchString(code) }
func Validate(r Rule) error {
	if r.Kind != "discount" && r.Kind != "gocoin_multiplier" {
		return errors.New("Choose a discount or GoCoin multiplier")
	}
	if r.Ends.IsZero() || r.Starts.IsZero() || !r.Ends.After(r.Starts) || len(r.Markets) == 0 || len(r.Categories) == 0 || len(r.Markets) > 100 || len(r.Categories) > 100 || r.StartHour < 0 || r.StartHour > 23 || r.EndHour > 24 || r.EndHour <= r.StartHour || r.MaxUses < 1 || r.MaxUses > 1000000 || r.PerRider < 1 || r.PerRider > 100 {
		return errors.New("Set dates, coverage, hours and usage limits")
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil {
		return errors.New("Choose a valid IANA timezone")
	}
	if r.Code != "" && !ValidCode(r.Code) {
		return errors.New("Codes require 4–32 letters, numbers, underscores or hyphens")
	}
	if r.Kind == "discount" && (r.Discount < 1 || r.Discount > 100000000 || r.Multiplier != 1 || r.Budget < r.Discount) {
		return errors.New("Set a positive discount and sufficient campaign budget")
	}
	if r.Kind == "gocoin_multiplier" && (r.Discount != 0 || r.Multiplier < 2 || r.Multiplier > 10) {
		return errors.New("GoCoin multiplier must be between 2 and 10")
	}
	if len(r.Zones) > MaxZones {
		return errors.New("An offer can cover at most 20 zones")
	}
	seen := map[string]bool{}
	for _, z := range r.Zones {
		if _, err := uuid.Parse(z); err != nil || seen[z] {
			return errors.New("Zones must be unique zone ids")
		}
		seen[z] = true
	}
	return nil
}

// MaxZones matches the rider_offers_zone_ids_limit constraint.
const MaxZones = 20

// ZoneScopeSQL is a boolean SQL expression over a rider_offers row: true when
// the offer has no zone scope, or the pickup of the fare named by fareParam
// (a placeholder such as "$5") lies inside one of the offer's zones. Quoting
// and reserving both use it so they cannot disagree. A zone that has been
// retired or is no longer approved stops matching, so the offer fails closed.
func ZoneScopeSQL(fareParam string) string {
	return `(cardinality(rider_offers.zone_ids)=0 OR EXISTS(SELECT 1 FROM ride_fares zf JOIN trip_geofences zg ON zg.id=ANY(rider_offers.zone_ids) WHERE zf.id=` + fareParam + `::uuid AND zg.kind IN('region','airport') AND zg.status='approved' AND zg.effective_from<=NOW() AND (zg.effective_until IS NULL OR zg.effective_until>NOW()) AND ST_Covers(zg.boundary,zf.pickup::geometry)))`
}

// ApprovedZones reports whether every zone is an approved region or airport
// that lies inside one of the markets and stays effective until the offer ends.
func ApprovedZones(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, zones, markets []string, until time.Time) (bool, error) {
	if len(zones) == 0 {
		return true, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*)::int FROM trip_geofences z WHERE z.id=ANY($1::text[]::uuid[]) AND z.kind IN('region','airport') AND z.status='approved' AND z.effective_from<=NOW() AND (z.effective_until IS NULL OR z.effective_until>=$3)
 AND EXISTS(SELECT 1 FROM trip_geofences m WHERE m.kind='market' AND m.code=ANY($2::text[]) AND m.status='approved' AND m.effective_from<=NOW() AND (m.effective_until IS NULL OR m.effective_until>NOW()) AND ST_CoveredBy(z.boundary,m.boundary))`, zones, markets, until).Scan(&n)
	return err == nil && n == len(zones), err
}
func WithinWindow(r Rule, at time.Time) bool {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return false
	}
	h := at.In(loc).Hour()
	return r.Active && !at.Before(r.Starts) && at.Before(r.Ends) && h >= r.StartHour && h < r.EndHour
}

const Columns = `id::text,title,message,kind,discount_kobo,multiplier,COALESCE(code,''),starts_at,ends_at,market_codes,category_slugs,airport_only,zone_ids::text[],start_hour,end_hour,timezone,budget_kobo,max_uses,per_rider_limit,active`

func Scan(row interface{ Scan(...any) error }) (r Rule, err error) {
	err = row.Scan(&r.ID, &r.Title, &r.Message, &r.Kind, &r.Discount, &r.Multiplier, &r.Code, &r.Starts, &r.Ends, &r.Markets, &r.Categories, &r.AirportOnly, &r.Zones, &r.StartHour, &r.EndHour, &r.Timezone, &r.Budget, &r.MaxUses, &r.PerRider, &r.Active)
	return
}
func Read(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (Rule, error) {
	return Scan(q.QueryRow(ctx, `SELECT `+Columns+` FROM rider_offers WHERE id=$1::uuid`, id))
}

func Capacity(ctx context.Context, tx pgx.Tx, r Rule, rider string, discount int64) (bool, error) {
	var count, mine int
	var cost int64
	err := tx.QueryRow(ctx, `SELECT count(*)::int,count(*) FILTER(WHERE rider_id=$2::uuid)::int,COALESCE(sum(discount_kobo),0)::bigint FROM rider_offer_uses WHERE offer_id=$1::uuid AND status IN('reserved','redeemed')`, r.ID, rider).Scan(&count, &mine, &cost)
	return err == nil && count < r.MaxUses && mine < r.PerRider && (r.Kind != "discount" || cost < r.Budget && cost+discount <= r.Budget), err
}
