package auth

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"testing"
)

func TestInternalGRPCAuthentication(t *testing.T) {
	interceptor := UnaryServerInterceptor("secret")
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/trip.TripService/CreateTrip"}
	if _, err := interceptor(context.Background(), nil, info, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated status=%v", status.Code(err))
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(internalTokenHeader, "secret"))
	if _, err := interceptor(ctx, nil, info, handler); err != nil {
		t.Fatalf("authenticated request rejected: %v", err)
	}
}

func TestHealthCheckBypassesServiceToken(t *testing.T) {
	interceptor := UnaryServerInterceptor("secret")
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("health check rejected: %v", err)
	}
}
