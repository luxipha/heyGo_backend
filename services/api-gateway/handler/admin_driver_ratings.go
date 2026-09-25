package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminDriverRating struct {
	TripID       string     `json:"tripId"`
	RiderID      string     `json:"riderId"`
	DriverID     string     `json:"driverId"`
	DriverName   string     `json:"driverName"`
	Rating       int        `json:"rating"`
	Comment      string     `json:"comment"`
	ReviewStatus string     `json:"reviewStatus"`
	CreatedAt    time.Time  `json:"createdAt"`
	ReviewedBy   *string    `json:"reviewedBy,omitempty"`
	ReviewedAt   *time.Time `json:"reviewedAt,omitempty"`
}

func registerAdminDriverRatingRoutes(group *gin.RouterGroup, api *adminAPI) {
	group.GET("/driver-ratings", api.listDriverRatings)
	group.POST("/driver-ratings/:tripID/review", api.requireCSRF, api.markDriverRatingReviewed)
}

func (a *adminAPI) listDriverRatings(ctx *gin.Context) {
	status := ctx.DefaultQuery("status", "pending")
	if status != "pending" && status != "reviewed" && status != "all" {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Status must be pending, reviewed, or all")
		return
	}
	limit, err := parseDriverTripHistoryLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursor *driverTripHistoryCursor
	if value := ctx.Query("cursor"); value != "" {
		decoded, err := decodeDriverTripHistoryCursor(value)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Rating cursor is invalid")
			return
		}
		cursor = &decoded
	}
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT r.trip_id::TEXT,t.rider_id::TEXT,t.assigned_driver_id::TEXT,
		COALESCE(NULLIF(p.display_name,''),u.human_id),r.rating,r.comment,r.admin_review_status,r.created_at,
		r.admin_reviewed_by::TEXT,r.admin_reviewed_at
		FROM trip_ratings r JOIN trips t ON t.id=r.trip_id
		JOIN users u ON u.id=t.assigned_driver_id
		LEFT JOIN driver_profiles p ON p.driver_id=t.assigned_driver_id
		WHERE r.actor_id=t.rider_id AND r.subject_id=t.assigned_driver_id
		AND ($1='all' OR r.admin_review_status=$1)
		AND ($2::TIMESTAMPTZ IS NULL OR (r.created_at,r.trip_id)<($2,$3::UUID))
		ORDER BY r.created_at DESC,r.trip_id DESC LIMIT $4`, status, cursorTime, cursorID, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_ratings_unavailable", "Driver ratings are unavailable")
		return
	}
	defer rows.Close()
	items := make([]adminDriverRating, 0, limit+1)
	for rows.Next() {
		var item adminDriverRating
		if err := rows.Scan(&item.TripID, &item.RiderID, &item.DriverID, &item.DriverName, &item.Rating, &item.Comment, &item.ReviewStatus, &item.CreatedAt, &item.ReviewedBy, &item.ReviewedAt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "driver_ratings_unavailable", "Driver ratings are unavailable")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_ratings_unavailable", "Driver ratings are unavailable")
		return
	}
	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: last.CreatedAt, ID: last.TripID})
		nextCursor = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: nextCursor}})
}

func (a *adminAPI) markDriverRatingReviewed(ctx *gin.Context) {
	tripID, err := uuid.Parse(ctx.Param("tripID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(dbctx)
	var driverID string
	err = tx.QueryRow(dbctx, `UPDATE trip_ratings r SET admin_review_status='reviewed',admin_reviewed_by=$2::UUID,admin_reviewed_at=NOW()
		FROM trips t WHERE r.trip_id=$1::UUID AND r.trip_id=t.id AND r.actor_id=t.rider_id AND r.subject_id=t.assigned_driver_id
		AND r.rating=1 AND r.admin_review_status='pending' RETURNING r.subject_id::TEXT`, tripID, currentAdmin(ctx).ID).Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "rating_not_pending", "This one-star rating is not awaiting review")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err := adminAudit(ctx, tx, "driver_rating.reviewed", driverID, gin.H{"tripId": tripID.String(), "rating": 1}); err != nil {
		reviewFailure(ctx)
		return
	}
	if err := tx.Commit(dbctx); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID.String(), "reviewStatus": "reviewed"}})
}
