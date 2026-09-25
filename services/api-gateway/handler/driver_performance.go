package handler

import (
	"context"
	"net/http"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

const driverPerformanceMinimumRatings = 25

type driverPerformanceSummary struct {
	RatingAverage          *float64 `json:"ratingAverage"`
	RatingCount            int64    `json:"ratingCount"`
	TopPercent             *int     `json:"topPercent"`
	PercentileEligible     bool     `json:"percentileEligible"`
	RatedDriverCohortCount int64    `json:"ratedDriverCohortCount"`
	CompletedTrips         int64    `json:"completedTrips"`
}

func readDriverPerformance(ctx context.Context, pool *pgxpool.Pool, driver string) (driverPerformanceSummary, error) {
	var result driverPerformanceSummary
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
	result.PercentileEligible = result.RatingCount >= driverPerformanceMinimumRatings && result.TopPercent != nil
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
