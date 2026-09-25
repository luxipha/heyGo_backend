package types

import (
	pb "github.com/cprakhar/uber-clone/shared/proto/trip"
	"github.com/cprakhar/uber-clone/shared/types"
)

type PreviewTripRequest struct {
	Pickup      types.Coordinate `json:"pickup" binding:"required"`
	Destination types.Coordinate `json:"destination" binding:"required"`
}

// ToProto converts PreviewTripRequest to its protobuf representation
func (ptr *PreviewTripRequest) ToProto(riderID string) *pb.PreviewTripRequest {
	return &pb.PreviewTripRequest{
		RiderID: riderID,
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
	FareID string `json:"rideFareID" binding:"required"`
}

// ToProto converts TripStartRequest to its protobuf representation
func (tsr *TripStartRequest) ToProto(riderID string) *pb.CreateTripRequest {
	return &pb.CreateTripRequest{
		RiderID:    riderID,
		RideFareID: tsr.FareID,
	}
}
