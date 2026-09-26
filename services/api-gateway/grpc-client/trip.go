package grpcclient

import (
	"context"

	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"google.golang.org/grpc"
)

type tripServiceClient struct {
	Client pb.TripServiceClient
	conn   *grpc.ClientConn
}

// NewTripServiceClient creates a new gRPC client for the Trip Service.
func NewTripServiceClient() (*tripServiceClient, error) {
	tripServiceURL := env.GetString("TRIP_SERVICE_URL", "trip-service:9000")
	transportOptions, err := sharedauth.GRPCTransportOptions(context.Background(), env.GetString("TRIP_SERVICE_AUDIENCE", ""))
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(
		tripServiceURL,
		append(append(
			traces.DialOptionsWithTracing(), transportOptions...),
			grpc.WithUnaryInterceptor(sharedauth.UnaryClientInterceptor(env.GetString("INTERNAL_SERVICE_TOKEN", ""))),
		)...,
	)
	if err != nil {
		return nil, err
	}

	client := pb.NewTripServiceClient(conn)
	return &tripServiceClient{Client: client, conn: conn}, nil
}

// Close closes the gRPC connection.
func (c *tripServiceClient) Close() error {
	return c.conn.Close()
}
