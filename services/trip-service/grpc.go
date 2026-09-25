package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/luxipha/heyGo_backend/services/trip-service/handler"
	"github.com/luxipha/heyGo_backend/services/trip-service/service"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type gRPCServer struct {
	addr        string
	tripService service.TripService
	kfClient    *kafka.KafkaClient
	healthCheck func(context.Context) error
}

// NewgRPCServer creates a new gRPC server instance
func NewgRPCServer(addr string, tripService service.TripService, kfc *kafka.KafkaClient, healthCheck func(context.Context) error) *gRPCServer {
	return &gRPCServer{addr: addr, tripService: tripService, kfClient: kfc, healthCheck: healthCheck}
}

// run starts the gRPC server and listens for incoming requests
func (s *gRPCServer) run(ctx context.Context) error {
	// Start listening on the specified address
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}

	// gRPC server setup with observability
	srv := grpc.NewServer(
		append(traces.WithTracingInterceptors(), grpc.ChainUnaryInterceptor(sharedauth.UnaryServerInterceptor(env.GetString("INTERNAL_SERVICE_TOKEN", "")), logs.UnaryServerInterceptor()))...,
	)
	handler.NewgRPCHandler(srv, s.tripService)
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
				return
			case <-ticker.C:
				status := healthpb.HealthCheckResponse_SERVING
				checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				if s.healthCheck(checkCtx) != nil {
					status = healthpb.HealthCheckResponse_NOT_SERVING
				}
				cancel()
				healthServer.SetServingStatus("", status)
			}
		}
	}()

	// Graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	// Start serving
	logs.L().Infow("gRPC server running", "addr", s.addr)
	if err := srv.Serve(lis); err != nil && ctx.Err() == nil { // ignore error if context cancelled
		return fmt.Errorf("serve grpc %s: %w", s.addr, err)
	}
	return nil
}
