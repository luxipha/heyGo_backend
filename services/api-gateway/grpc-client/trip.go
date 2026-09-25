package grpcclient

import (
	sharedauth "github.com/cprakhar/uber-clone/shared/auth"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
	pb "github.com/cprakhar/uber-clone/shared/proto/trip"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type tripServiceClient struct {
	Client pb.TripServiceClient
	conn   *grpc.ClientConn
}

// NewTripServiceClient creates a new gRPC client for the Trip Service.
func NewTripServiceClient() (*tripServiceClient, error) {
	tripServiceURL := env.GetString("TRIP_SERVICE_URL", "trip-service:9000")
	conn, err := grpc.NewClient(
		tripServiceURL,
		append(
			traces.DialOptionsWithTracing(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
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
