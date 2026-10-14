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
	"google.golang.org/genproto/googleapis/rpc/errdetails"
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

	fares, err := h.svc.GenerateTripFares(ctx, req.GetRiderID(), route, pickupCoords, destinationCoords)
	if err != nil {
		if errors.Is(err, repo.ErrPricingUnavailable) {
			return nil, riderTripError(repo.ErrPricingUnavailable)
		}
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
		return nil, riderTripError(err)
	}

	trip, err := h.svc.CreateTrip(ctx, fare)
	if err != nil {
		if errors.Is(err, repo.ErrFareExpired) || errors.Is(err, repo.ErrFareForeign) || errors.Is(err, repo.ErrFareInvalid) || errors.Is(err, repo.ErrDebtChanged) {
			return nil, riderTripError(err)
		}
		if errors.Is(err, repo.ErrActiveRiderTrip) {
			return nil, status.Error(codes.AlreadyExists, "rider already has an active trip")
		}
		if errors.Is(err, repo.ErrRiderRestricted) {
			return nil, riderTripError(repo.ErrRiderRestricted)
		}
		if errors.Is(err, repo.ErrTripClassificationUnavailable) || errors.Is(err, repo.ErrPricingUnavailable) {
			return nil, riderTripError(repo.ErrTripClassificationUnavailable)
		}
		return nil, status.Errorf(codes.Internal, "failed to create trip: %v", err)
	}

	logs.L().Infof("Created trip and queued event for trip ID: %s", trip.ID)

	return &pb.CreateTripResponse{
		TripID: trip.ID,
	}, nil
}

func riderTripError(err error) error {
	code, reason := codes.Unavailable, "trip_service_unavailable"
	switch {
	case errors.Is(err, repo.ErrFareInvalid):
		code, reason = codes.NotFound, "fare_invalid"
	case errors.Is(err, repo.ErrFareForeign):
		code, reason = codes.PermissionDenied, "fare_foreign"
	case errors.Is(err, repo.ErrFareExpired):
		code, reason = codes.FailedPrecondition, "fare_expired"
	case errors.Is(err, repo.ErrDebtChanged):
		code, reason = codes.FailedPrecondition, "debt_changed"
	case errors.Is(err, repo.ErrRiderRestricted):
		code, reason = codes.PermissionDenied, "rider_restricted"
	case errors.Is(err, repo.ErrTripClassificationUnavailable) || errors.Is(err, repo.ErrPricingUnavailable):
		code, reason = codes.FailedPrecondition, "market_unavailable"
	}
	s, _ := status.New(code, reason).WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "heygo.rider"})
	return s.Err()
}
