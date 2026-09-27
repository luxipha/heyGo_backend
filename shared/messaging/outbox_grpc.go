package messaging

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

func OutboxUnaryServerInterceptor(pool *pgxpool.Pool, producer outboxPublisher) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		response, err := handler(ctx, req)
		if err != nil {
			return response, err
		}
		if _, drainErr := DrainOutbox(ctx, pool, producer, 100); drainErr != nil && !isRequestCancellation(drainErr) {
			log.Printf("outbox gRPC drain failed after %s: %v", info.FullMethod, drainErr)
		}
		return response, nil
	}
}
