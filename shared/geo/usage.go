package geo

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"os"
	"strings"
	"time"
)

// Counts are conservative HeyGo attempts, not a provider invoice. Provider
// failures/timeouts can be billable; usage elsewhere in the account is unknown.
type RouteUsage struct {
	Provider             string `json:"provider"`
	Attempts             int64  `json:"attempts"`
	Successes            int64  `json:"successes"`
	Failures             int64  `json:"failures"`
	Pending              int64  `json:"pending"`
	FreeAllowance        int64  `json:"freeAllowance"`
	Remaining            int64  `json:"remaining"`
	OverAllowance        int64  `json:"overAllowance"`
	Warning              bool   `json:"warning"`
	CredentialConfigured bool   `json:"credentialConfigured"`
}
type RouteUsageSummary struct {
	Month          string       `json:"month"`
	FirstTrackedAt *time.Time   `json:"firstTrackedAt"`
	Providers      []RouteUsage `json:"providers"`
}

func RoutingCredentialConfigured(provider string) bool {
	switch provider {
	case "google":
		return strings.TrimSpace(os.Getenv("GOOGLE_ROUTES_API_KEY")) != ""
	case "amap":
		return strings.TrimSpace(os.Getenv("AMAP_ROUTES_API_KEY")) != ""
	case "osrm":
		return true
	}
	return false
}
func summarizeUsage(row RouteUsage) RouteUsage {
	switch row.Provider {
	case "google":
		row.FreeAllowance = 5000
	case "amap":
		row.FreeAllowance = 10000
	}
	row.Pending = row.Attempts - row.Successes - row.Failures
	if row.FreeAllowance > 0 {
		row.Remaining = max(row.FreeAllowance-row.Attempts, 0)
		row.OverAllowance = max(row.Attempts-row.FreeAllowance, 0)
		row.Warning = row.Attempts >= row.FreeAllowance*8/10
	}
	row.CredentialConfigured = RoutingCredentialConfigured(row.Provider)
	return row
}
func ReadRouteUsage(ctx context.Context, pool *pgxpool.Pool) (RouteUsageSummary, error) {
	if pool == nil {
		return RouteUsageSummary{}, errors.New("routing usage unavailable")
	}
	var month string
	if err := pool.QueryRow(ctx, `SELECT to_char(NOW() AT TIME ZONE 'UTC','YYYY-MM')`).Scan(&month); err != nil {
		return RouteUsageSummary{}, err
	}
	result := RouteUsageSummary{Month: month, Providers: []RouteUsage{}}
	rows, err := pool.Query(ctx, `SELECT provider,SUM(attempts),SUM(successes),SUM(failures) FROM geo_route_daily_usage WHERE day >= ($1 || '-01')::DATE AND day < (($1 || '-01')::DATE + INTERVAL '1 month') GROUP BY provider`, month)
	if err != nil {
		return RouteUsageSummary{}, err
	}
	counts := map[string]RouteUsage{}
	for rows.Next() {
		var row RouteUsage
		if err = rows.Scan(&row.Provider, &row.Attempts, &row.Successes, &row.Failures); err != nil {
			rows.Close()
			return RouteUsageSummary{}, err
		}
		counts[row.Provider] = row
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RouteUsageSummary{}, err
	}
	if err = pool.QueryRow(ctx, `SELECT MIN(first_at) FROM geo_route_daily_usage`).Scan(&result.FirstTrackedAt); err != nil {
		return RouteUsageSummary{}, err
	}
	for _, provider := range []string{"google", "amap", "osrm"} {
		row := counts[provider]
		row.Provider = provider
		result.Providers = append(result.Providers, summarizeUsage(row))
	}
	return result, nil
}

type routeUsageRecorder interface {
	Begin(context.Context, string) (string, error)
	Finish(context.Context, string, string, bool) error
}
type databaseRouteUsage struct{ Pool *pgxpool.Pool }

func (d databaseRouteUsage) Begin(ctx context.Context, provider string) (string, error) {
	var day string
	err := d.Pool.QueryRow(ctx, `INSERT INTO geo_route_daily_usage(day,provider,attempts) VALUES((NOW() AT TIME ZONE 'UTC')::DATE,$1,1) ON CONFLICT(day,provider) DO UPDATE SET attempts=geo_route_daily_usage.attempts+1 RETURNING day::TEXT`, provider).Scan(&day)
	return day, err
}
func (d databaseRouteUsage) Finish(ctx context.Context, provider, day string, success bool) error {
	var ok, fail int64
	if success {
		ok = 1
	} else {
		fail = 1
	}
	_, err := d.Pool.Exec(ctx, `UPDATE geo_route_daily_usage SET successes=successes+$3,failures=failures+$4 WHERE day=$1::DATE AND provider=$2`, day, provider, ok, fail)
	return err
}

type meteredRoutes struct {
	Router   RouteService
	Recorder routeUsageRecorder
	Provider string
}

func (m meteredRoutes) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	if !a.Valid() || !b.Valid() {
		return nil, errors.New("invalid route coordinates")
	}
	day, err := m.Recorder.Begin(ctx, m.Provider)
	if err != nil {
		return nil, errors.New("routing usage tracking unavailable")
	}
	result, routeErr := m.Router.Route(ctx, a, b)
	// Preserve the original UTC bucket across midnight and persist failures even
	// when the requesting client disconnected. A process crash stays pending.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err = m.Recorder.Finish(recordCtx, m.Provider, day, routeErr == nil); err != nil {
		log.Print("routing usage outcome could not be recorded")
	}
	return result, routeErr
}
