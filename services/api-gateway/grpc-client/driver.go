package grpcclient

import (
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	pb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type driverServiceClient struct {
	conn   *grpc.ClientConn
	Client pb.DriverServiceClient
}

// NewDriverServiceClient creates a new gRPC client for the Driver Service.
func NewDriverServiceClient() (*driverServiceClient, error) {
	driverServiceURL := env.GetString("DRIVER_SERVICE_URL", "driver-service:9100")
	conn, err := grpc.NewClient(driverServiceURL,
		append(traces.DialOptionsWithTracing(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
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
