package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/zonepricing"
)

type riderAmendmentAPI struct {
	pool   *pgxpool.Pool
	router geo.RouteService
}
type amendmentStop struct {
	ID          string      `json:"id"`
	Place       agent.Place `json:"place"`
	CompletedAt *time.Time  `json:"completedAt,omitempty"`
}
type amendmentBasis struct {
	Discount                                          int64
	Version                                           int64
	Status, FareID, DriverID, PricingID, Market, Slug string
	Fare                                              int64
	Debt                                              int64
	Pickup, Origin                                    geo.Coordinate
	Destination                                       agent.Place
	Stops                                             []amendmentStop
}

func (a *riderAmendmentAPI) basis(ctx context.Context, trip, owner string) (amendmentBasis, error) {
	var b amendmentBasis
	var destination, stops []byte
	err := a.pool.QueryRow(ctx, `SELECT t.version,t.status,f.id::text,COALESCE(t.assigned_driver_id::text,''),COALESCE(f.pricing_version_id::text,''),COALESCE(f.pricing_market_code,''),f.package_slug,ROUND(f.total_fare_minor)::bigint,ST_Y(f.pickup::geometry),ST_X(f.pickup::geometry),f.destination_metadata,t.stops,t.no_show_debt_kobo,t.offer_discount_kobo FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::uuid AND t.rider_id=$2::uuid`, trip, owner).Scan(&b.Version, &b.Status, &b.FareID, &b.DriverID, &b.PricingID, &b.Market, &b.Slug, &b.Fare, &b.Pickup.Latitude, &b.Pickup.Longitude, &destination, &stops, &b.Debt, &b.Discount)
	if err != nil {
		return b, err
	}
	if b.Status != "accepted" && b.Status != "arrived" && b.Status != "started" {
		return b, errors.New("trip cannot be amended in its current state")
	}
	if b.PricingID == "" || json.Unmarshal(destination, &b.Destination) != nil || b.Destination.ID == "" || json.Unmarshal(stops, &b.Stops) != nil {
		return b, errors.New("trip pricing or destination is unavailable")
	}
	b.Origin = b.Pickup
	if b.Status == "started" {
		if err := a.pool.QueryRow(ctx, `SELECT ST_Y(location::geometry),ST_X(location::geometry) FROM driver_live_locations WHERE driver_id=$1::uuid AND recorded_at>=NOW()-INTERVAL '2 minutes' AND recorded_at<=NOW()+INTERVAL '10 seconds'`, b.DriverID).Scan(&b.Origin.Latitude, &b.Origin.Longitude); err != nil {
			return b, errors.New("fresh driver location required")
		}
	}
	return b, nil
}

func (a *riderAmendmentAPI) preview(c *gin.Context) {
	var body struct {
		Version       int64    `json:"tripVersion"`
		DestinationID string   `json:"destinationPlaceId"`
		StopIDs       []string `json:"stopPlaceIds"`
	}
	trip := c.Param("tripID")
	if _, err := uuid.Parse(trip); err != nil || c.ShouldBindJSON(&body) != nil || len(body.StopIDs) > 3 || len(body.DestinationID) > 256 {
		driverError(c, 400, "invalid_amendment", "Trip version, destination and up to three ordered stops are required")
		return
	}
	if a.pool == nil {
		accountError(c)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	b, err := a.basis(ctx, trip, riderAccountOwner(c))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(c, 409, "amendment_unavailable", "Trip state, pricing or fresh location is unavailable")
		return
	}
	if body.Version != b.Version {
		driverError(c, 409, "trip_changed", "Refresh the trip before editing")
		return
	}
	if body.DestinationID == "" {
		body.DestinationID = b.Destination.ID
	}
	resolve := func(id string) (agent.Place, error) {
		if id == "" || len(id) > 256 {
			return agent.Place{}, errors.New("invalid place")
		}
		result, err := callRideAgent(ctx, agent.Request{SchemaVersion: 1, PlaceID: id})
		if err != nil || result.Place == nil || result.State != "resolved" || !agent.ValidLocation(result.Place.Location) {
			return agent.Place{}, errors.New("place resolution unavailable")
		}
		p := *result.Place
		if _, err := amendmentGeofence(ctx, a.pool, "region", p.Location, true); err != nil {
			return p, errors.New("place is outside an unambiguous active region")
		}
		var market *string
		if err := a.pool.QueryRow(ctx, `SELECT driver_market_at(ST_SetSRID(ST_MakePoint($1,$2),4326)::geography)`, p.Location.Longitude, p.Location.Latitude).Scan(&market); err != nil || market == nil || *market != b.Market {
			return p, errors.New("all stops must remain inside the original active market")
		}
		return p, nil
	}
	dest, err := resolve(body.DestinationID)
	if err != nil {
		driverError(c, 409, "destination_unavailable", err.Error())
		return
	}
	stops := []amendmentStop{}
	seen := map[string]bool{dest.ID: true}
	for _, id := range body.StopIDs {
		if seen[id] {
			driverError(c, 400, "duplicate_stop", "Stops and destination must be distinct")
			return
		}
		seen[id] = true
		p, err := resolve(id)
		if err != nil {
			driverError(c, 409, "stop_unavailable", err.Error())
			return
		}
		stops = append(stops, amendmentStop{ID: uuid.NewString(), Place: p})
	}
	router := a.router
	if router == nil {
		router = geo.DatabaseRoutes{Pool: a.pool}
	}
	// Both alternatives use one origin snapshot and one estimate timestamp.
	estimatedAt := time.Now().UTC()
	currentRoute, err := amendmentRoute(ctx, router, b.Origin, b.Stops, b.Destination)
	if err != nil {
		driverError(c, 503, "route_unavailable", "Current remaining route is unavailable")
		return
	}
	route, err := amendmentRoute(ctx, router, b.Origin, stops, dest)
	if err != nil {
		driverError(c, 503, "route_unavailable", "Replacement route is unavailable")
		return
	}
	if currentRoute.Provider != route.Provider {
		driverError(c, 503, "route_unavailable", "Route provider changed; retry the comparison")
		return
	}
	var base, km, minute int64
	if err := a.pool.QueryRow(ctx, `SELECT base_kobo,per_km_kobo,per_minute_kobo FROM pricing_package_rates WHERE pricing_id=$1::uuid AND package_slug=$2`, b.PricingID, b.Slug).Scan(&base, &km, &minute); err != nil {
		accountError(c)
		return
	}
	zone, err := zonepricing.Match(ctx, a.pool, b.PricingID, b.Pickup.Latitude, b.Pickup.Longitude)
	if err != nil {
		accountError(c)
		return
	}
	fare := math.Round(float64(base) + route.Routes[0].Distance*float64(km)/1000 + route.Routes[0].Duration*float64(minute)/60)
	pricingBasis := "original_pricing_version_full_route"
	if b.Status == "started" {
		pricingBasis = "original_pricing_version_remaining_route_delta"

		delta := (route.Routes[0].Distance-currentRoute.Routes[0].Distance)*float64(km)/1000 + (route.Routes[0].Duration-currentRoute.Routes[0].Duration)*float64(minute)/60
		if zone != nil && zone.Kind == "multiplier" {
			delta *= float64(zone.Value) / 10000
		}
		fare = float64(b.Fare) + delta
	} else {
		var valid bool
		fare, valid = zonepricing.Apply(fare, zone)
		if !valid {
			driverError(c, 409, "fare_unavailable", "Replacement fare is outside pricing limits")
			return
		}
	}
	fare = math.Round(fare)
	if math.IsNaN(fare) || math.IsInf(fare, 0) || fare <= 0 || fare > 10000000 {
		driverError(c, 409, "fare_unavailable", "Replacement fare is outside pricing limits")
		return
	}
	id := uuid.NewString()
	rawRoute, _ := json.Marshal(route)
	rawDest, _ := json.Marshal(dest)
	rawStops, _ := json.Marshal(stops)
	var expires time.Time
	err = a.pool.QueryRow(ctx, `INSERT INTO trip_amendment_quotes(id,trip_id,rider_id,trip_version,trip_status,old_fare_id,destination,stops,route,origin,fare_kobo,previous_fare_kobo) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6::uuid,$7,$8,$9,ST_SetSRID(ST_MakePoint($10,$11),4326)::geography,$12,$13) RETURNING expires_at`, id, trip, riderAccountOwner(c), b.Version, b.Status, b.FareID, rawDest, rawStops, rawRoute, b.Origin.Longitude, b.Origin.Latitude, int64(fare), b.Fare).Scan(&expires)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"quoteId": id, "tripId": trip, "tripVersion": b.Version, "fareKobo": int64(fare), "previousFareKobo": b.Fare, "deltaKobo": int64(fare) - b.Fare, "noShowDebtKobo": b.Debt, "previousAmountDueKobo": b.Fare + b.Debt - min(b.Discount, b.Fare), "amountDueDeltaKobo": int64(fare) - min(b.Discount, int64(fare)) - b.Fare + min(b.Discount, b.Fare), "offerDiscountKobo": min(b.Discount, int64(fare)), "amountDueKobo": int64(fare) + b.Debt - min(b.Discount, int64(fare)), "currency": "NGN", "expiresAt": expires, "destination": dest, "stops": stops, "route": route, "requiresConfirmation": true, "pricingBasis": pricingBasis, "timing": amendmentTiming(estimatedAt, b.Status, currentRoute.Routes[0].Duration, route.Routes[0].Duration)}})
}

// Before pickup, durations compare the passenger routes only: driver approach and
// boarding time are unknown, so absolute arrival timestamps are omitted.
func amendmentTiming(at time.Time, status string, current, proposed float64) gin.H {
	timing := gin.H{"estimatedAt": at, "currentDurationSeconds": current, "proposedDurationSeconds": proposed, "deltaDurationSeconds": proposed - current, "basis": "pickup_departure", "includesStopWaitingTime": false}
	if status == "started" {
		timing["basis"] = "driver_location"
		timing["currentEstimatedArrivalAt"] = at.Add(time.Duration(current * float64(time.Second)))
		timing["proposedEstimatedArrivalAt"] = at.Add(time.Duration(proposed * float64(time.Second)))
	}
	return timing
}

func amendmentRoute(ctx context.Context, router geo.RouteService, origin geo.Coordinate, stops []amendmentStop, destination agent.Place) (*geo.RouteResponse, error) {
	points := []geo.Coordinate{origin}
	for _, stop := range stops {
		if stop.CompletedAt == nil {
			points = append(points, geo.Coordinate{Latitude: stop.Place.Location.Latitude, Longitude: stop.Place.Location.Longitude})
		}
	}
	points = append(points, geo.Coordinate{Latitude: destination.Location.Latitude, Longitude: destination.Location.Longitude})
	combined := &geo.RouteResponse{Routes: []geo.Route{{TrafficAware: true}}}
	for i := 1; i < len(points); i++ {
		if !points[i].Valid() {
			return nil, errors.New("invalid route point")
		}
		part, err := router.Route(ctx, points[i-1], points[i])
		if err != nil || part == nil || len(part.Routes) == 0 {
			return nil, errors.New("route unavailable")
		}
		r := part.Routes[0]
		if r.Distance < 0 || r.Duration < 0 || math.IsNaN(r.Distance+r.Duration) || math.IsInf(r.Distance+r.Duration, 0) || len(r.Geometry.Coordinates) < 2 {
			return nil, errors.New("invalid route")
		}
		if i > 1 && part.Provider != combined.Provider {
			return nil, errors.New("route provider changed")
		}
		combined.Provider = part.Provider
		sum := &combined.Routes[0]
		sum.Distance += r.Distance
		sum.Duration += r.Duration
		sum.StaticDuration += r.StaticDuration
		sum.TrafficAware = sum.TrafficAware && r.TrafficAware
		sum.Geometry.Coordinates = append(sum.Geometry.Coordinates, r.Geometry.Coordinates...)
		sum.Maneuvers = append(sum.Maneuvers, r.Maneuvers...)
	}
	return combined, nil
}

func tripOptionsEvent(ctx context.Context, tx pgx.Tx, trip, source, kind string, payload any) error {
	var rider string
	var driver *string
	if err := tx.QueryRow(ctx, `SELECT rider_id::text,assigned_driver_id::text FROM trips WHERE id=$1::uuid`, trip).Scan(&rider, &driver); err != nil {
		return err
	}
	recipients := []string{rider}
	if driver != nil && *driver != rider {
		recipients = append(recipients, *driver)
	}
	// Consistent lock order across concurrent participant event writers.
	if len(recipients) == 2 && recipients[0] > recipients[1] {
		recipients[0], recipients[1] = recipients[1], recipients[0]
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for _, id := range recipients {
		if _, err := messaging.AppendEventTx(ctx, tx, id, source, kind, raw); err != nil {
			return err
		}
	}
	return nil
}

func (a *riderAmendmentAPI) confirm(c *gin.Context) {
	var body struct {
		Confirmed bool `json:"confirmed"`
	}
	trip, quote := c.Param("tripID"), c.Param("quoteID")
	if _, err := uuid.Parse(trip); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID required")
		return
	}
	if _, err := uuid.Parse(quote); err != nil || c.ShouldBindJSON(&body) != nil || !body.Confirmed {
		driverError(c, 400, "confirmation_required", "Confirm the quoted route and price")
		return
	}
	if a.pool == nil {
		accountError(c)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(ctx)
	var version int64
	var state, fareID string
	err = tx.QueryRow(ctx, `SELECT version,status,ride_fare_id::text FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, trip, riderAccountOwner(c)).Scan(&version, &state, &fareID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	var expected int64
	var expectedState, oldFare string
	var expires time.Time
	var applied *time.Time
	var appliedVersion *int64
	var price int64
	var dest, stops, route []byte
	var origin geo.Coordinate
	err = tx.QueryRow(ctx, `SELECT trip_version,trip_status,old_fare_id::text,expires_at,applied_at,applied_version,fare_kobo,destination,stops,route,ST_Y(origin::geometry),ST_X(origin::geometry) FROM trip_amendment_quotes WHERE id=$1::uuid AND trip_id=$2::uuid AND rider_id=$3::uuid FOR UPDATE`, quote, trip, riderAccountOwner(c)).Scan(&expected, &expectedState, &oldFare, &expires, &applied, &appliedVersion, &price, &dest, &stops, &route, &origin.Latitude, &origin.Longitude)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "quote_not_found", "Amendment quote was not found")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	if applied != nil {
		c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": trip, "quoteId": quote, "status": "applied", "tripVersion": appliedVersion}})
		return
	}
	if expected != version || state != expectedState || fareID != oldFare || !expires.After(time.Now()) {
		driverError(c, 409, "amendment_expired", "Trip changed or quote expired; request a new preview")
		return
	}
	if state == "started" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_live_locations l JOIN trips t ON t.assigned_driver_id=l.driver_id WHERE t.id=$1::uuid AND l.recorded_at>=NOW()-INTERVAL '2 minutes' AND ST_DWithin(l.location,ST_SetSRID(ST_MakePoint($2,$3),4326)::geography,300))`, trip, origin.Longitude, origin.Latitude).Scan(&valid); err != nil || !valid {
			driverError(c, 409, "amendment_location_changed", "Driver moved; preview the amendment again")
			return
		}
	}
	var destination agent.Place
	if json.Unmarshal(dest, &destination) != nil {
		accountError(c)
		return
	}
	region, err := amendmentGeofence(ctx, tx, "region", destination.Location, true)
	if err != nil {
		driverError(c, 409, "destination_unavailable", "Destination region is ambiguous or inactive")
		return
	}
	airport, err := amendmentGeofence(ctx, tx, "airport", destination.Location, false)
	if err != nil {
		driverError(c, 409, "destination_unavailable", "Destination airport boundary is ambiguous")
		return
	}
	var validMarket bool
	if err := tx.QueryRow(ctx, `SELECT driver_market_at(ST_SetSRID(ST_MakePoint($2,$3),4326)::geography)=f.pricing_market_code FROM ride_fares f WHERE f.id=$1::uuid`, oldFare, destination.Location.Longitude, destination.Location.Latitude).Scan(&validMarket); err != nil || !validMarket {
		driverError(c, 409, "destination_unavailable", "Destination market changed; preview again")
		return
	}
	var confirmedStops []amendmentStop
	if json.Unmarshal(stops, &confirmedStops) != nil {
		accountError(c)
		return
	}
	for _, stop := range confirmedStops {
		if _, err := amendmentGeofence(ctx, tx, "region", stop.Place.Location, true); err != nil {
			driverError(c, 409, "stop_unavailable", "Stop region changed; preview again")
			return
		}
		if err := tx.QueryRow(ctx, `SELECT driver_market_at(ST_SetSRID(ST_MakePoint($2,$3),4326)::geography)=f.pricing_market_code FROM ride_fares f WHERE f.id=$1::uuid`, oldFare, stop.Place.Location.Longitude, stop.Place.Location.Latitude).Scan(&validMarket); err != nil || !validMarket {
			driverError(c, 409, "stop_unavailable", "Stop market changed; preview again")
			return
		}
	}
	// Preserve the original fare row for audit and single-use booking recovery.
	replacement := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,no_show_debt_kobo,pricing_version_id,pricing_market_code,pickup_metadata,destination_metadata,pickup_note,guest_name,guest_phone,ride_preferences,consumed_trip_id,consumed_at) SELECT $1::uuid,rider_id,package_slug,$3,$4,pickup,ST_SetSRID(ST_MakePoint($5,$6),4326)::geography,no_show_debt_kobo,pricing_version_id,pricing_market_code,pickup_metadata,$7,pickup_note,guest_name,guest_phone,ride_preferences,$8::uuid,NOW() FROM ride_fares WHERE id=$2::uuid`, replacement, oldFare, price, route, destination.Location.Longitude, destination.Location.Latitude, dest, trip)
	if err != nil {
		accountError(c)
		return
	}
	_, err = tx.Exec(ctx, `UPDATE trips SET ride_fare_id=$2::uuid,stops=$3,version=version+1,route_revision=route_revision+1,updated_at=NOW() WHERE id=$1::uuid`, trip, replacement, stops)
	if err == nil {
		regionRaw, _ := json.Marshal(region)
		airportRaw, _ := json.Marshal(airport)
		_, err = tx.Exec(ctx, `UPDATE trips SET destination_region_code=$2,regulatory_tags=ARRAY[CASE WHEN origin_region_code=$2 THEN 'LOCAL' ELSE 'INTERSTATE' END] || CASE WHEN 'AIRPORT_PICKUP'=ANY(regulatory_tags) THEN ARRAY['AIRPORT_PICKUP'] ELSE ARRAY[]::text[] END || CASE WHEN $4::jsonb<>'null'::jsonb THEN ARRAY['AIRPORT_DROPOFF'] ELSE ARRAY[]::text[] END,classification_geofences=jsonb_set(jsonb_set(COALESCE(classification_geofences,'{}'::jsonb),'{destinationRegion}',$3::jsonb),'{destinationAirport}',$4::jsonb),classified_at=NOW() WHERE id=$1::uuid`, trip, region["code"], regionRaw, airportRaw)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE trip_amendment_quotes SET applied_at=NOW(),applied_version=$2,replacement_fare_id=$3::uuid WHERE id=$1::uuid`, quote, version+1, replacement)
	}
	if err == nil {
		err = tripOptionsEvent(ctx, tx, trip, "amendment:"+quote, "trip.event.amended", gin.H{"tripId": trip, "tripVersion": version + 1, "quoteId": quote, "fareKobo": price, "currency": "NGN", "destination": json.RawMessage(dest), "stops": json.RawMessage(stops), "route": json.RawMessage(route), "refreshTrip": true})
	}
	if err != nil || tx.Commit(ctx) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": trip, "quoteId": quote, "status": "applied", "tripVersion": version + 1}})
}

func amendmentGeofence(ctx context.Context, tx pickupETAStore, kind string, point agent.Coordinate, required bool) (map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,code,version FROM trip_geofences WHERE kind=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()) AND ST_Covers(boundary,ST_SetSRID(ST_MakePoint($2,$3),4326)) LIMIT 2`, kind, point.Longitude, point.Latitude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var match map[string]any
	for rows.Next() {
		if match != nil {
			return nil, errors.New("ambiguous boundary")
		}
		var id, code string
		var version int
		if err := rows.Scan(&id, &code, &version); err != nil {
			return nil, err
		}
		match = map[string]any{"id": id, "code": code, "version": version}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if required && match == nil {
		return nil, errors.New("missing boundary")
	}
	return match, nil
}

func (a *riderAmendmentAPI) completeStop(c *gin.Context) {
	var body struct {
		Version int64 `json:"tripVersion"`
	}
	if _, err := uuid.Parse(c.Param("tripID")); err != nil || c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_stop", "Valid trip and version required")
		return
	}
	if a.pool == nil {
		accountError(c)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var version int64
	err = tx.QueryRow(ctx, `SELECT stops,version FROM trips WHERE id=$1::uuid AND assigned_driver_id=$2::uuid AND status='started' FOR UPDATE`, c.Param("tripID"), driverID(c)).Scan(&raw, &version)
	if err != nil {
		driverError(c, 404, "trip_not_found", "Active assigned trip was not found")
		return
	}
	var stops []amendmentStop
	if json.Unmarshal(raw, &stops) != nil {
		accountError(c)
		return
	}
	for i := range stops {
		if stops[i].ID == c.Param("stopID") && stops[i].CompletedAt != nil {
			c.JSON(200, contracts.APIResponse{Data: gin.H{"completed": true, "tripVersion": version}})
			return
		}
		if stops[i].CompletedAt != nil {
			continue
		}
		if stops[i].ID != c.Param("stopID") || version != body.Version {
			driverError(c, 409, "stop_changed", "Refresh the trip and complete stops in order")
			return
		}
		var nearby bool
		p := stops[i].Place.Location
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_live_locations WHERE driver_id=$1::uuid AND recorded_at>=NOW()-INTERVAL '2 minutes' AND ST_DWithin(location,ST_SetSRID(ST_MakePoint($2,$3),4326)::geography,150))`, driverID(c), p.Longitude, p.Latitude).Scan(&nearby)
		if err != nil || !nearby {
			driverError(c, 409, "stop_not_reached", "A fresh driver location within 150 metres is required")
			return
		}
		now := time.Now().UTC()
		stops[i].CompletedAt = &now
		encoded, _ := json.Marshal(stops)
		_, err = tx.Exec(ctx, `UPDATE trips SET stops=$2,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, c.Param("tripID"), encoded)
		if err == nil {
			err = tripOptionsEvent(ctx, tx, c.Param("tripID"), fmt.Sprintf("stop:%s:%s", c.Param("tripID"), stops[i].ID), "trip.event.stop_completed", gin.H{"tripId": c.Param("tripID"), "tripVersion": version + 1, "stops": stops, "refreshTrip": true})
		}
		if err != nil || tx.Commit(ctx) != nil {
			accountError(c)
			return
		}
		c.JSON(200, contracts.APIResponse{Data: gin.H{"completed": true, "tripVersion": version + 1}})
		return
	}
	driverError(c, 404, "stop_not_found", "Stop was not found")
}
