package handler

import (
	"net/http"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type driverReceivedReview struct {
	TripID    string    `json:"tripId"`
	Rating    int       `json:"rating"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"createdAt"`
}

func readDriverReceivedReviews(ctx *gin.Context, pool *pgxpool.Pool, driver string, limit int, cursor *driverTripHistoryCursor) ([]driverReceivedReview, error) {
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	rows, err := pool.Query(ctx.Request.Context(), `SELECT r.trip_id::TEXT,r.rating,r.comment,r.created_at
		FROM trip_ratings r JOIN trips t ON t.id=r.trip_id
		WHERE r.subject_id=$1::UUID AND t.assigned_driver_id=$1::UUID AND t.status='completed'
		AND r.actor_id=t.rider_id
		AND ($2::TIMESTAMPTZ IS NULL OR (r.created_at,r.trip_id)<($2,$3::UUID))
		ORDER BY r.created_at DESC,r.trip_id DESC LIMIT $4`, driver, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]driverReceivedReview, 0, limit+1)
	for rows.Next() {
		var item driverReceivedReview
		if err := rows.Scan(&item.TripID, &item.Rating, &item.Comment, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.CreatedAt = item.CreatedAt.UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *driverAPI) driverReviews(ctx *gin.Context) {
	limit, err := parseDriverTripHistoryLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursor *driverTripHistoryCursor
	if value := ctx.Query("cursor"); value != "" {
		decoded, err := decodeDriverTripHistoryCursor(value)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Review cursor is invalid")
			return
		}
		cursor = &decoded
	}
	items, err := readDriverReceivedReviews(ctx, a.pool, driverID(ctx), limit, cursor)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "reviews_unavailable", "Driver reviews are unavailable")
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
