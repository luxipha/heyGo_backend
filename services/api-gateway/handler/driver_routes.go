package handler

import (
	"errors"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type driverRouteCoordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type driverRouteRequest struct {
	TripID      string                `json:"tripId"`
	Origin      driverRouteCoordinate `json:"origin"`
	Destination driverRouteCoordinate `json:"destination"`
}

func validRouteCoordinate(point driverRouteCoordinate) bool {
	return !math.IsNaN(point.Latitude) && !math.IsNaN(point.Longitude) && !math.IsInf(point.Latitude, 0) && !math.IsInf(point.Longitude, 0) && validCoordinate(point.Latitude, point.Longitude)
}

func (a *driverAPI) route(ctx *gin.Context) {
	var body driverRouteRequest
	if ctx.ShouldBindJSON(&body) != nil || !validRouteCoordinate(body.Origin) || !validRouteCoordinate(body.Destination) {
		driverError(ctx, http.StatusBadRequest, "invalid_route_request", "Valid origin and destination are required")
		return
	}
	if _, err := uuid.Parse(body.TripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var status string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT status FROM trips WHERE id=$1::UUID AND assigned_driver_id=$2::UUID AND status IN('assigned','accepted','started')`, body.TripID, driverID(ctx)).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Active assigned trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "route_unavailable", "Driver route is unavailable")
		return
	}
	router := geo.DatabaseRoutes{Pool: a.pool}
	result, err := router.Route(ctx.Request.Context(), geo.Coordinate{Latitude: body.Origin.Latitude, Longitude: body.Origin.Longitude}, geo.Coordinate{Latitude: body.Destination.Latitude, Longitude: body.Destination.Longitude})
	if err != nil {
		driverError(ctx, http.StatusBadGateway, "route_unavailable", "Routing service is unavailable")
		return
	}
	route := result.Routes[0]
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": body.TripID, "polyline": geo.Polyline(route.Geometry.Coordinates), "distanceMeters": route.Distance, "etaSeconds": route.Duration, "maneuvers": route.Maneuvers, "provider": result.Provider}})
}
