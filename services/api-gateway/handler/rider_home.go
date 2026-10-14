package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/tripstate"
)

type riderHomeDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type riderHomeAPI struct{ pool riderHomeDB }

type riderTripSnapshot struct {
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	Version          int64           `json:"version"`
	PackageSlug      string          `json:"packageSlug"`
	FareKobo         int64           `json:"fareKobo"`
	NoShowDebtKobo   int64           `json:"noShowDebtKobo"`
	AmountDueKobo    int64           `json:"amountDueKobo"`
	Currency         string          `json:"currency"`
	Route            json.RawMessage `json:"route"`
	Pickup           json.RawMessage `json:"pickup"`
	Destination      json.RawMessage `json:"destination"`
	PickupPlace      json.RawMessage `json:"pickupPlace"`
	DestinationPlace json.RawMessage `json:"destinationPlace"`

	MatchingDeadline *time.Time      `json:"matchingDeadline"`
	MatchingOutcome  *string         `json:"matchingOutcome"`
	Assignment       json.RawMessage `json:"assignment"`

	Driver             json.RawMessage `json:"driver"`
	SettlementStatus   *string         `json:"settlementStatus"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
	ArrivedAt          *time.Time      `json:"arrivedAt"`
	StartedAt          *time.Time      `json:"startedAt"`
	CompletedAt        *time.Time      `json:"completedAt"`
	CancelledAt        *time.Time      `json:"cancelledAt"`
	CancellationReason *string         `json:"cancellationReason"`
}

func registerRiderHomeRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool) {
	a := &riderHomeAPI{}
	if pool != nil {
		a.pool = pool
	}
	rider := authenticated.Group("/rider", gatewayauth.RequireRole("rider"))
	rider.GET("/trips/active", a.activeTrip)
	rider.GET("/trips/:tripID", a.tripDetail)
	rider.GET("/availability", a.availability)
	rider.GET("/fares/:fareID/trip", a.fareTrip)
}

// Recover unfinished transport and completed trips awaiting direct settlement.
// Driver assertions are exposed as settlement status, never provider payment proof.
func (a *riderHomeAPI) readTrip(ctx *gin.Context, id string, active bool) (*riderTripSnapshot, error) {
	if a.pool == nil {
		return nil, errors.New("database unavailable")
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	where := `t.id=$2::UUID`
	args := []any{user.ID, id}
	if active {
		where = tripstate.RiderRecoverableSQL
		args = args[:1]
	}
	var t riderTripSnapshot
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT t.id::TEXT,t.status,t.version,f.package_slug,
 ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo,ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,f.route,f.pickup_metadata,f.destination_metadata,t.matching_deadline,t.matching_outcome,
 (SELECT jsonb_build_object('id',a.trip_id::TEXT||':'||a.attempt::TEXT,'attempt',a.attempt,'status',a.status,'expiresAt',a.expires_at,'pickupEtaSeconds',NULL,'pickupEtaStatus','unavailable','vehicle',CASE WHEN a.status='accepted' THEN jsonb_build_object('make',v.make,'model',v.model,'color',v.color,'plate',v.plate,'packageSlug',v.package_slug) ELSE NULL END) FROM driver_assignments a LEFT JOIN driver_vehicles v ON v.driver_id=a.driver_id WHERE a.trip_id=t.id AND a.driver_id=t.assigned_driver_id AND a.status IN ('offered','accepted') ORDER BY a.attempt DESC LIMIT 1),
 CASE WHEN f.pickup IS NULL THEN 'null'::jsonb ELSE jsonb_build_object('latitude',ST_Y(f.pickup::geometry),'longitude',ST_X(f.pickup::geometry)) END,
 CASE WHEN f.destination IS NULL THEN 'null'::jsonb ELSE jsonb_build_object('latitude',ST_Y(f.destination::geometry),'longitude',ST_X(f.destination::geometry)) END,
 CASE WHEN t.status IN ('accepted','arrived','started','completed') AND d.id IS NOT NULL
 THEN jsonb_build_object('id',d.id,'name',d.name,'profilePic',d.profile_pic,'carPlate',d.car_plate,
 'location',CASE WHEN t.status IN ('accepted','arrived','started') AND l.recorded_at>=NOW()-INTERVAL '2 minutes' THEN jsonb_build_object('latitude',ST_Y(l.location::geometry),'longitude',ST_X(l.location::geometry),'recordedAt',l.recorded_at) ELSE NULL END)
 ELSE 'null'::jsonb END,s.status,t.created_at,t.updated_at,t.arrived_at,t.started_at,t.completed_at,t.cancelled_at,t.cancellation_reason
 FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id
 LEFT JOIN drivers d ON d.id=t.assigned_driver_id
 LEFT JOIN driver_live_locations l ON l.driver_id=d.id
 LEFT JOIN trip_settlements s ON s.trip_id=t.id
 WHERE t.rider_id=$1::UUID AND `+where+` ORDER BY t.created_at DESC,t.id DESC LIMIT 1`, args...).Scan(
		&t.ID, &t.Status, &t.Version, &t.PackageSlug, &t.FareKobo, &t.NoShowDebtKobo, &t.AmountDueKobo, &t.Route, &t.PickupPlace, &t.DestinationPlace, &t.MatchingDeadline, &t.MatchingOutcome, &t.Assignment, &t.Pickup, &t.Destination, &t.Driver, &t.SettlementStatus, &t.CreatedAt, &t.UpdatedAt, &t.ArrivedAt, &t.StartedAt, &t.CompletedAt, &t.CancelledAt, &t.CancellationReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Currency = "NGN"
	return &t, nil
}
func (a *riderHomeAPI) activeTrip(ctx *gin.Context) {
	if a.pool == nil {
		driverError(ctx, 503, "trip_unavailable", "Your current trip is unavailable")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	// Tail first, then snapshot: concurrent changes may be replayed twice, but
	// events committed between these reads cannot be skipped by subscription.
	var cursor int64
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT COALESCE(MAX(id),0) FROM user_events WHERE recipient_id=$1::UUID`, user.ID).Scan(&cursor); err != nil {
		driverError(ctx, 503, "trip_unavailable", "Your current trip is unavailable")
		return
	}

	t, err := a.readTrip(ctx, "", true)
	if err != nil {
		driverError(ctx, 503, "trip_unavailable", "Your current trip is unavailable")
		return
	}
	ctx.JSON(200, contracts.APIResponse{Data: gin.H{"trip": t, "afterEventId": fmt.Sprintf("evt_%d", cursor)}})
}
func (a *riderHomeAPI) tripDetail(ctx *gin.Context) {
	id := ctx.Param("tripID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(ctx, 400, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	t, err := a.readTrip(ctx, id, false)
	if err != nil {
		driverError(ctx, 503, "trip_unavailable", "Trip is unavailable")
		return
	}
	if t == nil {
		driverError(ctx, 404, "trip_not_found", "Trip was not found")
		return
	}
	ctx.JSON(200, contracts.APIResponse{Data: t})
}
func (a *riderHomeAPI) availability(ctx *gin.Context) {
	lat, errLat := strconv.ParseFloat(ctx.Query("latitude"), 64)
	lng, errLng := strconv.ParseFloat(ctx.Query("longitude"), 64)
	slug := ctx.Query("packageSlug")
	if errLat != nil || errLng != nil || !validCoordinate(lat, lng) || slug == "" {
		driverError(ctx, 400, "invalid_availability_query", "Coordinates and packageSlug are required")
		return
	}
	if a.pool == nil {
		driverError(ctx, 503, "availability_unavailable", "Nearby availability is unavailable")
		return
	}
	var market *string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT driver_market_at(ST_SetSRID(ST_MakePoint($1,$2),4326)::geography)`, lng, lat).Scan(&market)
	if err != nil {
		driverError(ctx, 503, "availability_unavailable", "Nearby availability is unavailable")
		return
	}
	if market == nil || *market == "" {
		driverError(ctx, 409, "market_unavailable", "Pickup is outside an unambiguous active service market")
		return
	}
	var validPackage bool
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT COUNT(*)=1 FROM pricing_package_rates r JOIN pricing_versions v ON v.id=r.pricing_id WHERE v.market_code=$1 AND v.status='approved' AND v.effective_from<=NOW() AND (v.effective_until IS NULL OR v.effective_until>NOW()) AND r.package_slug=$2`, *market, slug).Scan(&validPackage)
	if err != nil {
		driverError(ctx, 503, "availability_unavailable", "Nearby availability is unavailable")
		return
	}
	if !validPackage {
		driverError(ctx, 400, "package_unavailable", "Package is unavailable for this market")
		return
	}
	var count int64
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT COUNT(*) FROM drivers d JOIN driver_eligibility e ON e.driver_id=d.id JOIN driver_live_locations l ON l.driver_id=d.id WHERE `+driverstate.AvailabilityPredicate("$1", "$2", "ST_SetSRID(ST_MakePoint($4,$3),4326)::geography", "$5"), *market, slug, lat, lng, driverstate.MatchingRadiusMeters).Scan(&count)
	if err != nil {
		driverError(ctx, 503, "availability_unavailable", "Nearby availability is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"count": count, "packageSlug": slug, "marketCode": *market, "radiusMeters": driverstate.MatchingRadiusMeters, "observedAt": time.Now().UTC(), "locationFreshnessSeconds": 1800}})
}

// An owned read recovers a confirmed request after a lost start response.
func (a *riderHomeAPI) fareTrip(ctx *gin.Context) {
	id := ctx.Param("fareID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(ctx, 400, "fare_invalid", "Valid fare ID is required")
		return
	}
	if a.pool == nil {
		driverError(ctx, 503, "trip_unavailable", "Trip is unavailable")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	var tripID *string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT consumed_trip_id::TEXT FROM ride_fares WHERE id=$1::UUID AND rider_id=$2::UUID`, id, user.ID).Scan(&tripID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, 404, "fare_invalid", "Fare was not found")
		return
	}
	if err != nil {
		driverError(ctx, 503, "trip_unavailable", "Trip is unavailable")
		return
	}
	var trip *riderTripSnapshot
	if tripID != nil {
		trip, err = a.readTrip(ctx, *tripID, false)
		if err == nil && trip == nil {
			driverError(ctx, 404, "fare_invalid", "Fare was not found")
			return
		}
	}
	if err != nil {
		driverError(ctx, 503, "trip_unavailable", "Trip is unavailable")
		return
	}
	ctx.JSON(200, contracts.APIResponse{Data: gin.H{"trip": trip}})
}
