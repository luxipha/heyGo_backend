package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffTrip struct {
	ID          string     `json:"id"`
	RiderID     string     `json:"riderId"`
	DriverID    *string    `json:"driverId"`
	RideFareID  string     `json:"rideFareId"`
	Status      string     `json:"status"`
	MarketCode  *string    `json:"marketCode"`
	FareKobo    int64      `json:"fareKobo"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	CancelledAt *time.Time `json:"cancelledAt,omitempty"`
	Links       gin.H      `json:"links,omitempty"`
}

type staffRider struct {
	ID             string    `json:"id"`
	Active         bool      `json:"active"`
	TripRestricted bool      `json:"tripRestricted"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Links          gin.H     `json:"links,omitempty"`
}

func registerAdminTripRiderRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/trips", a.listStaffTrips)
	group.GET("/trips/:tripID", a.staffTripDetail)
	group.GET("/riders", a.listStaffRiders)
	group.GET("/riders/:riderID", a.staffRiderDetail)
}

func staffPage(ctx *gin.Context) (int, any, any, bool) {
	limit, err := parseDriverTripHistoryLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return 0, nil, nil, false
	}
	if ctx.Query("cursor") == "" {
		return limit, nil, nil, true
	}
	cursor, err := decodeDriverTripHistoryCursor(ctx.Query("cursor"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Cursor is invalid")
		return 0, nil, nil, false
	}
	return limit, cursor.OccurredAt, cursor.ID, true
}

func staffTripStatus(ctx *gin.Context) (string, bool) {
	status := ctx.DefaultQuery("status", "all")
	switch status {
	case "all", "pending", "assigned", "accepted", "arrived", "started", "completed", "cancelled":
		return status, true
	default:
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Trip status is invalid")
		return "", false
	}
}

const staffTripFields = `t.id::TEXT,t.rider_id::TEXT,t.assigned_driver_id::TEXT,t.ride_fare_id::TEXT,t.status,t.market_code,ROUND(f.total_fare_minor)::BIGINT,t.created_at,t.updated_at,t.started_at,t.completed_at,t.cancelled_at`
const staffTripFrom = ` FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id `

func scanStaffTrip(row pgx.Row) (staffTrip, error) {
	var item staffTrip
	err := row.Scan(&item.ID, &item.RiderID, &item.DriverID, &item.RideFareID, &item.Status, &item.MarketCode, &item.FareKobo, &item.CreatedAt, &item.UpdatedAt, &item.StartedAt, &item.CompletedAt, &item.CancelledAt)
	return item, err
}

func (a *adminAPI) listStaffTrips(ctx *gin.Context) {
	status, ok := staffTripStatus(ctx)
	if !ok {
		return
	}
	var riderID, driverID any
	if raw := ctx.Query("riderId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_rider_id", "Valid rider ID is required")
			return
		}
		riderID = id
	}
	if raw := ctx.Query("driverId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid driver ID is required")
			return
		}
		driverID = id
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT `+staffTripFields+staffTripFrom+`WHERE ($1='all' OR t.status=$1) AND ($2::TIMESTAMPTZ IS NULL OR (t.created_at,t.id)<($2,$3::UUID)) AND ($5::UUID IS NULL OR t.rider_id=$5) AND ($6::UUID IS NULL OR t.assigned_driver_id=$6) ORDER BY t.created_at DESC,t.id DESC LIMIT $4`, status, at, id, limit+1, riderID, driverID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trips_unavailable", "Trips are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffTrip, 0, limit+1)
	for rows.Next() {
		item, err := scanStaffTrip(rows)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "trips_unavailable", "Trips are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trips_unavailable", "Trips are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffTripDetail(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("tripID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	item, err := scanStaffTrip(a.pool.QueryRow(ctx.Request.Context(), `SELECT `+staffTripFields+staffTripFrom+`WHERE t.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trips_unavailable", "Trip is unavailable")
		return
	}
	detail, err := a.staffTripLinks(ctx, id)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trips_unavailable", "Trip links are unavailable")
		return
	}
	item.Links = detail
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}

func (a *adminAPI) listStaffRiders(ctx *gin.Context) {
	// Search uses exact internal IDs. Contact-based lookup needs a separate
	// permission and explicit product decision.
	var searchID any
	if raw := strings.TrimSpace(ctx.Query("q")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_search", "Search requires a Rider, Trip, or Support case ID")
			return
		}
		searchID = parsed
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,active,rider_trip_restricted,created_at,updated_at FROM users u WHERE roles @> ARRAY['rider']::TEXT[] AND ($1::TIMESTAMPTZ IS NULL OR (created_at,id)<($1,$2::UUID)) AND ($4::UUID IS NULL OR u.id=$4 OR EXISTS(SELECT 1 FROM trips t WHERE t.id=$4 AND t.rider_id=u.id) OR EXISTS(SELECT 1 FROM support_cases c WHERE c.id=$4 AND c.opened_by=u.id AND c.audience='rider')) ORDER BY created_at DESC,id DESC LIMIT $3`, at, id, limit+1, searchID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "riders_unavailable", "Riders are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffRider, 0, limit+1)
	for rows.Next() {
		var item staffRider
		if rows.Scan(&item.ID, &item.Active, &item.TripRestricted, &item.CreatedAt, &item.UpdatedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "riders_unavailable", "Riders are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "riders_unavailable", "Riders are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffRiderDetail(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("riderID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_rider_id", "Valid rider ID is required")
		return
	}
	var item staffRider
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,active,rider_trip_restricted,created_at,updated_at FROM users WHERE id=$1 AND roles @> ARRAY['rider']::TEXT[]`, id).Scan(&item.ID, &item.Active, &item.TripRestricted, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "rider_not_found", "Rider was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "riders_unavailable", "Rider is unavailable")
		return
	}
	detail, err := a.staffRiderLinks(ctx, id)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "riders_unavailable", "Rider links are unavailable")
		return
	}
	item.Links = detail
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}
