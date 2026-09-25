package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type driverTripDetail struct {
	ID                      string          `json:"id"`
	RiderID                 string          `json:"riderId"`
	Status                  string          `json:"status"`
	PackageSlug             string          `json:"packageSlug"`
	FareKobo                int64           `json:"fareKobo"`
	NoShowDebtKobo          int64           `json:"noShowDebtKobo"`
	AmountDueKobo           int64           `json:"amountDueKobo"`
	Currency                string          `json:"currency"`
	Route                   json.RawMessage `json:"route"`
	CreatedAt               time.Time       `json:"createdAt"`
	StartedAt               *time.Time      `json:"startedAt"`
	ArrivedAt               *time.Time      `json:"arrivedAt"`
	CompletedAt             *time.Time      `json:"completedAt"`
	CancelledAt             *time.Time      `json:"cancelledAt"`
	CancellationReason      *string         `json:"cancellationReason"`
	EarningsKobo            *int64          `json:"earningsKobo"`
	MarketCode              *string         `json:"marketCode"`
	OriginRegionCode        *string         `json:"originRegionCode"`
	DestinationRegionCode   *string         `json:"destinationRegionCode"`
	RegulatoryTags          []string        `json:"regulatoryTags"`
	ClassificationGeofences json.RawMessage `json:"classificationGeofences"`
}

func (a *driverAPI) tripDetail(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var trip driverTripDetail
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT t.id::TEXT,t.rider_id::TEXT,t.status,f.package_slug,
		ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo,ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,f.route,t.created_at,t.arrived_at,t.started_at,t.completed_at,t.cancelled_at,
		t.cancellation_reason,e.earnings_kobo,t.market_code,t.origin_region_code,
		t.destination_region_code,t.regulatory_tags,t.classification_geofences
		FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id
		LEFT JOIN driver_trip_earnings e ON e.trip_id=t.id
		WHERE t.id=$1::UUID AND t.assigned_driver_id=$2::UUID`, tripID, driverID(ctx)).Scan(
		&trip.ID, &trip.RiderID, &trip.Status, &trip.PackageSlug, &trip.FareKobo, &trip.NoShowDebtKobo, &trip.AmountDueKobo,
		&trip.Route, &trip.CreatedAt, &trip.ArrivedAt, &trip.StartedAt, &trip.CompletedAt,
		&trip.CancelledAt, &trip.CancellationReason, &trip.EarningsKobo,
		&trip.MarketCode, &trip.OriginRegionCode, &trip.DestinationRegionCode,
		&trip.RegulatoryTags, &trip.ClassificationGeofences,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trip_unavailable", "Trip is unavailable")
		return
	}
	trip.Currency = "NGN"
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: trip})
}
