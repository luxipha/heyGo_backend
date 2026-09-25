package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var driverRouteHTTPClient = &http.Client{Timeout: 10 * time.Second}

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
	base := strings.TrimSuffix(env.GetString("OSRM_API", "http://router.project-osrm.org/route/v1/driving"), "/")
	routeURL, err := url.Parse(fmt.Sprintf("%s/%.6f,%.6f;%.6f,%.6f", base, body.Origin.Longitude, body.Origin.Latitude, body.Destination.Longitude, body.Destination.Latitude))
	if err != nil || (routeURL.Scheme != "http" && routeURL.Scheme != "https") {
		driverError(ctx, http.StatusServiceUnavailable, "route_unavailable", "Routing service is not configured")
		return
	}
	query := routeURL.Query()
	query.Set("steps", "true")
	query.Set("overview", "full")
	query.Set("geometries", "polyline")
	routeURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodGet, routeURL.String(), nil)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "route_unavailable", "Driver route is unavailable")
		return
	}
	response, err := driverRouteHTTPClient.Do(request)
	if err != nil {
		driverError(ctx, http.StatusBadGateway, "routing_service_unavailable", "Routing service is unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		driverError(ctx, http.StatusBadGateway, "routing_service_unavailable", "Routing service is unavailable")
		return
	}
	var result struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry string  `json:"geometry"`
			Legs     []struct {
				Steps []struct {
					Distance float64 `json:"distance"`
					Duration float64 `json:"duration"`
					Name     string  `json:"name"`
					Maneuver struct {
						Type     string    `json:"type"`
						Modifier string    `json:"modifier"`
						Location []float64 `json:"location"`
					} `json:"maneuver"`
				} `json:"steps"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil || result.Code != "Ok" || len(result.Routes) == 0 || result.Routes[0].Geometry == "" {
		driverError(ctx, http.StatusBadGateway, "route_unavailable", "Routing service returned no usable route")
		return
	}
	route := result.Routes[0]
	maneuvers := make([]gin.H, 0)
	for _, leg := range route.Legs {
		for _, step := range leg.Steps {
			maneuvers = append(maneuvers, gin.H{"type": step.Maneuver.Type, "modifier": step.Maneuver.Modifier, "roadName": step.Name, "location": step.Maneuver.Location, "distanceMeters": step.Distance, "durationSeconds": step.Duration})
		}
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": body.TripID, "polyline": route.Geometry, "distanceMeters": route.Distance, "etaSeconds": route.Duration, "maneuvers": maneuvers}})
}
