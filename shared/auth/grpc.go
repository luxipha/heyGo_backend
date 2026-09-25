package auth

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/cprakhar/uber-clone/shared/observe/correlation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const internalTokenHeader = "x-internal-service-token"

func UnaryServerInterceptor(expectedToken string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return handler(ctx, req)
		}
		values := metadata.ValueFromIncomingContext(ctx, internalTokenHeader)
		if expectedToken == "" || len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(expectedToken)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "service authentication required")
		}
		if ids := metadata.ValueFromIncomingContext(ctx, "x-correlation-id"); len(ids) == 1 {
			ctx = correlation.WithID(ctx, ids[0])
		}
		return handler(ctx, req)
	}
}

func UnaryClientInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, internalTokenHeader, token)
		if id := correlation.FromContext(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-correlation-id", id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
