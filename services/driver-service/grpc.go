package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/handler"
	"github.com/luxipha/heyGo_backend/services/driver-service/service"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type gRPCServer struct {
	addr          string
	bus           *pubsub.Client
	driverService service.DriverService
	healthCheck   func(context.Context) error
	pushHandler   http.Handler
}

func NewgRPCServer(addr string, bus *pubsub.Client, svc service.DriverService, healthCheck func(context.Context) error, pushHandler http.Handler) *gRPCServer {
	return &gRPCServer{addr: addr, bus: bus, driverService: svc, healthCheck: healthCheck, pushHandler: pushHandler}
}

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
	handler.NewgRPCHandler(srv, s.driverService)
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

	handler := h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			srv.ServeHTTP(w, r)
			return
		}
		if s.pushHandler != nil {
			s.pushHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}), &http2.Server{})
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		srv.GracefulStop()
	}()

	logs.L().Infow("gRPC and internal HTTP server running", "addr", s.addr)
	if err := httpServer.Serve(lis); err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
		return fmt.Errorf("failed to serve gRPC/HTTP on %s: %w", s.addr, err)
	}
	return nil
}
