package types

import (
	"fmt"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"github.com/luxipha/heyGo_backend/shared/types"
	"time"
)

type PreviewTripRequest struct {
	PickupAt           string `json:"pickupAt"`
	ForScheduled       bool   `json:"forScheduled"`
	PickupNote         string `json:"pickupNote"`
	SourceTripID       string `json:"sourceTripId"`
	SchemaVersion      int    `json:"schemaVersion"`
	PickupPlaceID      string `json:"pickupPlaceId"`
	DestinationPlaceID string `json:"destinationPlaceId"`
	// Pin destinations use coordinates directly, without a provider place ID.
	DestinationSource string           `json:"destinationSource"`
	Pickup            types.Coordinate `json:"pickup" binding:"required"`
	Destination       types.Coordinate `json:"destination" binding:"required"`
}

// ToProto converts PreviewTripRequest to its protobuf representation
func (ptr *PreviewTripRequest) ToProto(riderID string) *pb.PreviewTripRequest {
	return &pb.PreviewTripRequest{
		RiderID:  riderID,
		PickupAt: ptr.PickupAt,
		Pickup: &pb.Coordinate{
			Latitude:  ptr.Pickup.Latitude,
			Longitude: ptr.Pickup.Longitude,
		},
		Destination: &pb.Coordinate{
			Latitude:  ptr.Destination.Latitude,
			Longitude: ptr.Destination.Longitude,
		},
	}
}

type TripStartRequest struct {
	FareID    string `json:"rideFareID" binding:"required"`
	Confirmed bool   `json:"confirmed"`
}

// ToProto converts TripStartRequest to its protobuf representation
func (tsr *TripStartRequest) ToProto(riderID string) *pb.CreateTripRequest {
	return &pb.CreateTripRequest{
		RiderID:    riderID,
		RideFareID: tsr.FareID,
	}
}

// ValidatePickupTime prevents scheduled previews silently using today's rates.
func (ptr *PreviewTripRequest) ValidatePickupTime(now time.Time) error {
	if !ptr.ForScheduled {
		if ptr.PickupAt != "" {
			return fmt.Errorf("pickupAt requires forScheduled")
		}
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, ptr.PickupAt)
	if err != nil || !at.After(now) {
		return fmt.Errorf("Scheduled previews require a future pickupAt with a timezone")
	}
	return nil
}
