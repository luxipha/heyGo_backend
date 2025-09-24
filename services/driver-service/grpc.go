package main

import (
	"context"
	"fmt"
	"net"

	"github.com/cprakhar/uber-clone/services/driver-service/handler"
	"github.com/cprakhar/uber-clone/services/driver-service/service"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
	"google.golang.org/grpc"
)

type gRPCServer struct {
	addr          string
	kfClient      *kafka.KafkaClient
	driverService service.DriverService
}

func NewgRPCServer(addr string, kfc *kafka.KafkaClient, svc service.DriverService) *gRPCServer {
	return &gRPCServer{addr: addr, kfClient: kfc, driverService: svc}
}

func (s *gRPCServer) run(ctx context.Context) error {
	// Start listening on the specified address
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}

	// gRPC server setup with observability
	srv := grpc.NewServer(
		traces.WithTracingInterceptors()...,
	)
	handler.NewgRPCHandler(srv, s.driverService)

	// Graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	// Start serving
	logs.L().Infow("gRPC server running", "addr", s.addr)
	if err := srv.Serve(lis); err != nil && ctx.Err() == nil {
		return fmt.Errorf("failed to serve gRPC on %s: %w", s.addr, err)
	}
	return nil
}
