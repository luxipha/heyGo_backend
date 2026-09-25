package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"github.com/jackc/pgx/v5"
)

var ErrTripClassificationUnavailable = errors.New("trip location does not match one unambiguous approved geofence")

type geofenceMatch struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Version int    `json:"version"`
}

type tripClassification struct {
	MarketCode            string
	OriginRegionCode      string
	DestinationRegionCode string
	Tags                  []string
	Geofences             []byte
	ClassifiedAt          time.Time
}

func matchingGeofence(ctx context.Context, tx pgx.Tx, kind string, point *sharedtypes.Coordinate, at time.Time, required bool) (*geofenceMatch, error) {
	if point == nil {
		return nil, ErrTripClassificationUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT id::TEXT,code,version FROM trip_geofences
		WHERE kind=$1 AND status='approved' AND effective_from<=$2
		AND (effective_until IS NULL OR effective_until>$2)
		AND ST_Covers(boundary,ST_SetSRID(ST_MakePoint($3,$4),4326))
		LIMIT 2`, kind, at, point.Longitude, point.Latitude)
	if err != nil {
		return nil, fmt.Errorf("find %s geofence: %w", kind, err)
	}
	defer rows.Close()
	var matches []geofenceMatch
	for rows.Next() {
		var match geofenceMatch
		if err := rows.Scan(&match.ID, &match.Code, &match.Version); err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) > 1 || (required && len(matches) == 0) {
		return nil, ErrTripClassificationUnavailable
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

func classifyTrip(ctx context.Context, tx pgx.Tx, fare *triptypes.RideFareModel, at time.Time) (tripClassification, error) {
	if fare.Pickup == nil || fare.Destination == nil {
		return tripClassification{}, ErrTripClassificationUnavailable
	}
	market, err := matchingGeofence(ctx, tx, "market", fare.Pickup, at, true)
	if err != nil {
		return tripClassification{}, err
	}
	origin, err := matchingGeofence(ctx, tx, "region", fare.Pickup, at, true)
	if err != nil {
		return tripClassification{}, err
	}
	destination, err := matchingGeofence(ctx, tx, "region", fare.Destination, at, true)
	if err != nil {
		return tripClassification{}, err
	}
	pickupAirport, err := matchingGeofence(ctx, tx, "airport", fare.Pickup, at, false)
	if err != nil {
		return tripClassification{}, err
	}
	destinationAirport, err := matchingGeofence(ctx, tx, "airport", fare.Destination, at, false)
	if err != nil {
		return tripClassification{}, err
	}
	tags := make([]string, 0, 3)
	if origin.Code != destination.Code {
		tags = append(tags, "INTERSTATE")
	} else {
		tags = append(tags, "LOCAL")
	}
	if pickupAirport != nil {
		tags = append(tags, "AIRPORT_PICKUP")
	}
	if destinationAirport != nil {
		tags = append(tags, "AIRPORT_DROPOFF")
	}
	snapshot, err := json.Marshal(struct {
		Market             *geofenceMatch `json:"market"`
		OriginRegion       *geofenceMatch `json:"originRegion"`
		DestinationRegion  *geofenceMatch `json:"destinationRegion"`
		PickupAirport      *geofenceMatch `json:"pickupAirport"`
		DestinationAirport *geofenceMatch `json:"destinationAirport"`
	}{market, origin, destination, pickupAirport, destinationAirport})
	if err != nil {
		return tripClassification{}, err
	}
	return tripClassification{MarketCode: market.Code, OriginRegionCode: origin.Code,
		DestinationRegionCode: destination.Code, Tags: tags, Geofences: snapshot, ClassifiedAt: at}, nil
}
