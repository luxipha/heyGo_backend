package handler

import (
	"context"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

const (
	driverPerformanceMinimumRatings = 25
	// A rank among a handful of drivers says little (one ranked driver would
	// be "Top 100%"), so the percentile waits for a minimum group size.
	driverPerformanceMinimumCohort = 5
	driverStandingWindowDays       = 30
)

// driverStandingOffers counts ride offers the driver had to answer. An offer
// withdrawn before the driver responded is not their decision and is left out.
type driverStandingOffers struct {
	Decided           int64 `json:"decided"`
	Accepted          int64 `json:"accepted"`
	Declined          int64 `json:"declined"`
	TimedOut          int64 `json:"timedOut"`
	PickRatePercent   *int  `json:"pickRatePercent"`
	IgnoreRatePercent *int  `json:"ignoreRatePercent"`
}

type driverStandingTrips struct {
	Assigned                int64 `json:"assigned"`
	DriverCancelled         int64 `json:"driverCancelled"`
	CancellationRatePercent *int  `json:"cancellationRatePercent"`
}

type driverStandingTrust struct {
	Score  *int    `json:"score"`
	Status *string `json:"status"`
}

// driverStandingScheduled holds raw scheduled-ride outcomes. No penalty or
// threshold is applied here; that policy is not decided yet.
type driverStandingScheduled struct {
	Completed     int64 `json:"completed"`
	CheckedIn     int64 `json:"checkedIn"`
	Released      int64 `json:"released"`
	MissedCheckIn int64 `json:"missedCheckIn"`
	NotAvailable  int64 `json:"notAvailable"`
}

type driverPerformanceSummary struct {
	RatingAverage          *float64 `json:"ratingAverage"`
	RatingCount            int64    `json:"ratingCount"`
	TopPercent             *int     `json:"topPercent"`
	PercentileEligible     bool     `json:"percentileEligible"`
	RatedDriverCohortCount int64    `json:"ratedDriverCohortCount"`
	CompletedTrips         int64    `json:"completedTrips"`
	// "ratings" (fewer than MinimumRatings of their own) or "cohort" (too few
	// ranked drivers); empty when the percentile is shown.
	PercentileUnavailableReason string `json:"percentileUnavailableReason,omitempty"`
	MinimumRatings              int    `json:"minimumRatings"`

	WindowDays int                     `json:"windowDays"`
	Offers     driverStandingOffers    `json:"offers"`
	Trips      driverStandingTrips     `json:"trips"`
	Trust      driverStandingTrust     `json:"trust"`
	Scheduled  driverStandingScheduled `json:"scheduled"`
}

func standingPercent(part, whole int64) *int {
	if whole <= 0 {
		return nil
	}
	v := int(math.Round(float64(part) * 100 / float64(whole)))
	return &v
}

func readDriverPerformance(ctx context.Context, pool *pgxpool.Pool, driver string) (driverPerformanceSummary, error) {
	result := driverPerformanceSummary{MinimumRatings: driverPerformanceMinimumRatings, WindowDays: driverStandingWindowDays}
	err := pool.QueryRow(ctx, `WITH rider_ratings AS (
		SELECT t.assigned_driver_id AS driver_id,AVG(r.rating)::NUMERIC AS average_rating,COUNT(*)::BIGINT AS rating_count
		FROM trip_ratings r JOIN trips t ON t.id=r.trip_id
		JOIN users u ON u.id=t.assigned_driver_id
		WHERE r.actor_id=t.rider_id AND r.subject_id=t.assigned_driver_id AND t.status='completed'
		AND u.roles @> ARRAY['driver']::TEXT[]
		GROUP BY t.assigned_driver_id
	), eligible AS (
		SELECT driver_id,average_rating,rating_count FROM rider_ratings WHERE rating_count >= $2
	), ranked AS (
		SELECT driver_id,RANK() OVER (ORDER BY average_rating DESC) AS rank_position,
		COUNT(*) OVER ()::BIGINT AS cohort_count FROM eligible
	)
	SELECT (SELECT ROUND(average_rating,2)::DOUBLE PRECISION FROM rider_ratings WHERE driver_id=$1::UUID),
		COALESCE((SELECT rating_count FROM rider_ratings WHERE driver_id=$1::UUID),0),
		(SELECT CEIL(rank_position * 100.0 / NULLIF(cohort_count,0))::INTEGER FROM ranked WHERE driver_id=$1::UUID),
		COALESCE((SELECT cohort_count FROM ranked WHERE driver_id=$1::UUID),0),
		(SELECT COUNT(*)::BIGINT FROM trips WHERE assigned_driver_id=$1::UUID AND status='completed')`, driver, driverPerformanceMinimumRatings).Scan(
		&result.RatingAverage, &result.RatingCount, &result.TopPercent, &result.RatedDriverCohortCount, &result.CompletedTrips)
	if err != nil {
		return driverPerformanceSummary{}, err
	}
	switch {
	case result.RatingCount < driverPerformanceMinimumRatings || result.TopPercent == nil:
		result.PercentileUnavailableReason = "ratings"
	case result.RatedDriverCohortCount < driverPerformanceMinimumCohort:
		result.PercentileUnavailableReason = "cohort"
	default:
		result.PercentileEligible = true
	}
	if !result.PercentileEligible {
		result.TopPercent = nil
	}

	o, t, s := &result.Offers, &result.Trips, &result.Scheduled
	err = pool.QueryRow(ctx, `WITH offers AS (
		SELECT a.status,(t.assigned_driver_id IS NOT DISTINCT FROM a.driver_id) AS took_trip
		FROM driver_assignments a JOIN trips t ON t.id=a.trip_id
		WHERE a.driver_id=$1::UUID AND a.offered_at>=NOW()-make_interval(days=>$2)
	), outcomes AS (
		SELECT outcome FROM driver_scheduled_outcomes
		WHERE driver_id=$1::UUID AND recorded_at>=NOW()-make_interval(days=>$2)
	)
	SELECT
		(SELECT COUNT(*) FROM offers WHERE status IN ('accepted','completed') OR (status='cancelled' AND took_trip)),
		(SELECT COUNT(*) FROM offers WHERE status='declined'),
		(SELECT COUNT(*) FROM offers WHERE status='timed_out'),
		(SELECT COUNT(*) FROM trips WHERE assigned_driver_id=$1::UUID AND created_at>=NOW()-make_interval(days=>$2)),
		(SELECT COUNT(*) FROM trips WHERE assigned_driver_id=$1::UUID AND status='cancelled' AND cancellation_actor_id=$1::UUID AND created_at>=NOW()-make_interval(days=>$2)),
		(SELECT provider_trust_score FROM users WHERE id=$1::UUID),
		(SELECT NULLIF(provider_trust_status,'') FROM users WHERE id=$1::UUID),
		(SELECT COUNT(*) FROM scheduled_rides WHERE driver_id=$1::UUID AND status='completed' AND pickup_at>=NOW()-make_interval(days=>$2)),
		(SELECT COUNT(*) FROM outcomes WHERE outcome='checked_in'),
		(SELECT COUNT(*) FROM outcomes WHERE outcome='released'),
		(SELECT COUNT(*) FROM outcomes WHERE outcome='missed_check_in'),
		(SELECT COUNT(*) FROM outcomes WHERE outcome='not_available')`,
		driver, driverStandingWindowDays).Scan(
		&o.Accepted, &o.Declined, &o.TimedOut, &t.Assigned, &t.DriverCancelled,
		&result.Trust.Score, &result.Trust.Status,
		&s.Completed, &s.CheckedIn, &s.Released, &s.MissedCheckIn, &s.NotAvailable)
	if err != nil {
		return driverPerformanceSummary{}, err
	}
	o.Decided = o.Accepted + o.Declined + o.TimedOut
	o.PickRatePercent = standingPercent(o.Accepted, o.Decided)
	o.IgnoreRatePercent = standingPercent(o.TimedOut, o.Decided)
	t.CancellationRatePercent = standingPercent(t.DriverCancelled, t.Assigned)
	return result, nil
}

func (a *driverAPI) driverPerformance(ctx *gin.Context) {
	result, err := readDriverPerformance(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "performance_unavailable", "Driver performance is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: result})
}
