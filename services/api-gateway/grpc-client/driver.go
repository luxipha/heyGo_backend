package grpcclient

import (
	"context"

	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	pb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	"google.golang.org/grpc"
)

type driverServiceClient struct {
	conn   *grpc.ClientConn
	Client pb.DriverServiceClient
}

// NewDriverServiceClient creates a new gRPC client for the Driver Service.
func NewDriverServiceClient() (*driverServiceClient, error) {
	driverServiceURL := env.GetString("DRIVER_SERVICE_URL", "driver-service:9100")
	transportOptions, err := sharedauth.GRPCTransportOptions(context.Background(), env.GetString("DRIVER_SERVICE_AUDIENCE", ""))
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(driverServiceURL,
		append(append(traces.DialOptionsWithTracing(), transportOptions...),
			grpc.WithUnaryInterceptor(sharedauth.UnaryClientInterceptor(env.GetString("INTERNAL_SERVICE_TOKEN", ""))))...,
	)
	if err != nil {
		return nil, err
	}

	client := pb.NewDriverServiceClient(conn)
	return &driverServiceClient{Client: client, conn: conn}, nil
}

// Close closes the gRPC connection.
func (c *driverServiceClient) Close() error {
	return c.conn.Close()
}
