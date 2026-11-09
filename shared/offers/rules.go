package offers

import (
	"context"
	"errors"
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
	StartHour   int       `json:"startHour"`
	EndHour     int       `json:"endHour"`
	Timezone    string    `json:"timezone"`
	Budget      int64     `json:"budgetKobo"`
	MaxUses     int       `json:"maxUses"`
	PerRider    int       `json:"perRiderLimit"`
	Active      bool      `json:"active"`
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
	return nil
}
func WithinWindow(r Rule, at time.Time) bool {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return false
	}
	h := at.In(loc).Hour()
	return r.Active && !at.Before(r.Starts) && at.Before(r.Ends) && h >= r.StartHour && h < r.EndHour
}

const Columns = `id::text,title,message,kind,discount_kobo,multiplier,COALESCE(code,''),starts_at,ends_at,market_codes,category_slugs,airport_only,start_hour,end_hour,timezone,budget_kobo,max_uses,per_rider_limit,active`

func Scan(row interface{ Scan(...any) error }) (r Rule, err error) {
	err = row.Scan(&r.ID, &r.Title, &r.Message, &r.Kind, &r.Discount, &r.Multiplier, &r.Code, &r.Starts, &r.Ends, &r.Markets, &r.Categories, &r.AirportOnly, &r.StartHour, &r.EndHour, &r.Timezone, &r.Budget, &r.MaxUses, &r.PerRider, &r.Active)
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
