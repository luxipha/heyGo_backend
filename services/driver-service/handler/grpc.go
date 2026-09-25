package handler

import (
	"context"

	"github.com/luxipha/heyGo_backend/services/driver-service/service"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	pb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type gRPCHandler struct {
	pb.UnimplementedDriverServiceServer
	svc service.DriverService
}

func NewgRPCHandler(srv *grpc.Server, svc service.DriverService) {
	handler := &gRPCHandler{svc: svc}
	pb.RegisterDriverServiceServer(srv, handler)
}

func (h *gRPCHandler) RegisterDriver(ctx context.Context, req *pb.RegisterDriverRequest) (*pb.RegisterDriverResponse, error) {
	driverID := req.GetDriverID()
	packageSlug := req.GetPackageSlug()
	// Implement the logic to register a driver
	driver, err := h.svc.RegisterDriver(ctx, driverID, packageSlug)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to register driver: %v", err)
	}
	logs.L().Infow("Driver registered", "driverID", driver.Id)
	return &pb.RegisterDriverResponse{
		Driver: driver,
	}, nil
}

func (h *gRPCHandler) UnregisterDriver(ctx context.Context, req *pb.RegisterDriverRequest) (*pb.RegisterDriverResponse, error) {
	if err := h.svc.UnregisterDriver(ctx, req.GetDriverID()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unregister driver: %v", err)
	}
	logs.L().Infow("Driver unregistered", "driverID", req.GetDriverID())

	return &pb.RegisterDriverResponse{
		Driver: &pb.Driver{
			Id: req.GetDriverID(),
		},
	}, nil
}
