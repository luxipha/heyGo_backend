package handler

import (
	"context"
	"errors"

	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"github.com/luxipha/heyGo_backend/services/trip-service/service"
	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type gRPCHandler struct {
	pb.UnimplementedTripServiceServer
	svc service.TripService
}

// NewgRPCHandler registers the gRPC handler with the given gRPC server
func NewgRPCHandler(srv *grpc.Server, svc service.TripService) {
	handler := &gRPCHandler{svc: svc}
	pb.RegisterTripServiceServer(srv, handler)
}

// PreviewTrip handles the PreviewTrip gRPC request
func (h *gRPCHandler) PreviewTrip(ctx context.Context, req *pb.PreviewTripRequest) (*pb.PreviewTripResponse, error) {
	pickup := req.GetPickup()
	destination := req.GetDestination()

	pickupCoords := &sharedtypes.Coordinate{
		Latitude:  pickup.GetLatitude(),
		Longitude: pickup.GetLongitude(),
	}
	destinationCoords := &sharedtypes.Coordinate{
		Latitude:  destination.GetLatitude(),
		Longitude: destination.GetLongitude(),
	}

	route, err := h.svc.GetRoute(ctx, pickupCoords, destinationCoords)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get route: %v", err)
	}

	estimatedFares := h.svc.EstimatePackagesPriceWithRoute(route)

	fares, err := h.svc.GenerateTripFares(ctx, estimatedFares, req.GetRiderID(), route, pickupCoords, destinationCoords)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate trip fares: %v", err)
	}

	return &pb.PreviewTripResponse{
		Route:     route.ToProto(),
		RideFares: types.ToRideFaresProto(fares),
	}, nil
}

// CreateTrip handles the CreateTrip gRPC request
func (h *gRPCHandler) CreateTrip(ctx context.Context, req *pb.CreateTripRequest) (*pb.CreateTripResponse, error) {
	fareID := req.GetRideFareID()
	riderID := req.GetRiderID()
	fare, err := h.svc.GetAndValidateRideFare(ctx, fareID, riderID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid fare: %v", err)
	}

	trip, err := h.svc.CreateTrip(ctx, fare)
	if err != nil {
		if errors.Is(err, repo.ErrTripClassificationUnavailable) {
			return nil, status.Error(codes.FailedPrecondition, "pickup or destination is outside an approved market or has overlapping geofences")
		}
		return nil, status.Errorf(codes.Internal, "failed to create trip: %v", err)
	}

	logs.L().Infof("Created trip and queued event for trip ID: %s", trip.ID)

	return &pb.CreateTripResponse{
		TripID: trip.ID,
	}, nil
}
