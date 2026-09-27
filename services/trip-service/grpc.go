package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/trip-service/handler"
	"github.com/luxipha/heyGo_backend/services/trip-service/service"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging"
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
	addr        string
	tripService service.TripService
	bus         *pubsub.Client
	healthCheck func(context.Context) error
	pushHandler http.Handler
	pool        *pgxpool.Pool
}

// NewgRPCServer creates a new gRPC server instance
func NewgRPCServer(addr string, tripService service.TripService, bus *pubsub.Client, healthCheck func(context.Context) error, pushHandler http.Handler, pool *pgxpool.Pool) *gRPCServer {
	return &gRPCServer{addr: addr, tripService: tripService, bus: bus, healthCheck: healthCheck, pushHandler: pushHandler, pool: pool}
}

// run starts the gRPC server and listens for incoming requests
func (s *gRPCServer) run(ctx context.Context) error {
	// Start listening on the specified address
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}

	// gRPC server setup with observability
	interceptors := []grpc.UnaryServerInterceptor{
		sharedauth.UnaryServerInterceptor(env.GetString("INTERNAL_SERVICE_TOKEN", "")),
		logs.UnaryServerInterceptor(),
	}
	if s.pool != nil {
		interceptors = append(interceptors, messaging.OutboxUnaryServerInterceptor(s.pool, s.bus.Producer))
	}
	srv := grpc.NewServer(append(traces.WithTracingInterceptors(), grpc.ChainUnaryInterceptor(interceptors...))...)
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
		return fmt.Errorf("serve grpc/http %s: %w", s.addr, err)
	}
	return nil
}
