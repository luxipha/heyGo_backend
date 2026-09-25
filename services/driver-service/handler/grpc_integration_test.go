package handler

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/repo"
	driverservice "github.com/luxipha/heyGo_backend/services/driver-service/service"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	driverpb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type grpcDriverRepo struct{ online map[string]*driverpb.Driver }

func (r *grpcDriverRepo) UpsertOnline(_ context.Context, driver *driverpb.Driver) (*driverpb.Driver, error) {
	r.online[driver.Id] = driver
	return driver, nil
}
func (r *grpcDriverRepo) SetOffline(_ context.Context, id string) error {
	delete(r.online, id)
	return nil
}
func (r *grpcDriverRepo) UpdateLocation(context.Context, string, float64, float64) (string, error) {
	return "", nil
}
func (r *grpcDriverRepo) MatchAndReserve(context.Context, string, string, float64, float64, float64, time.Duration, []byte) (*repo.Candidate, error) {
	return nil, repo.ErrNoAvailableDriver
}
func (r *grpcDriverRepo) DeclineAssignment(context.Context, string, string) (*repo.RetryTrip, error) {
	return nil, nil
}
func (r *grpcDriverRepo) ExpireOffers(context.Context, int) ([]repo.RetryTrip, error) {
	return nil, nil
}

func TestDriverGRPCAuthenticationAndLifecycle(t *testing.T) {
	if _, err := logs.Init("driver-grpc-integration-test"); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	repository := &grpcDriverRepo{online: make(map[string]*driverpb.Driver)}
	server := grpc.NewServer(grpc.UnaryInterceptor(sharedauth.UnaryServerInterceptor("service-secret")))
	NewgRPCHandler(server, driverservice.NewDriverService(repository))
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	dialer := func(context.Context, string) (net.Conn, error) { return listener.Dial() }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	authedConn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(sharedauth.UnaryClientInterceptor("service-secret")))
	if err != nil {
		t.Fatalf("dial authenticated client: %v", err)
	}
	defer authedConn.Close()
	client := driverpb.NewDriverServiceClient(authedConn)
	registered, err := client.RegisterDriver(ctx, &driverpb.RegisterDriverRequest{DriverID: "driver-1", PackageSlug: "sedan"})
	if err != nil {
		t.Fatalf("register driver RPC: %v", err)
	}
	if registered.Driver.Id != "driver-1" || repository.online["driver-1"] == nil {
		t.Fatalf("driver was not registered: %#v", registered.Driver)
	}
	if _, err := client.UnregisterDriver(ctx, &driverpb.RegisterDriverRequest{DriverID: "driver-1"}); err != nil {
		t.Fatalf("unregister driver RPC: %v", err)
	}
	if repository.online["driver-1"] != nil {
		t.Fatal("driver remained online after unregister RPC")
	}

	unauthenticatedConn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauthenticated client: %v", err)
	}
	defer unauthenticatedConn.Close()
	_, err = driverpb.NewDriverServiceClient(unauthenticatedConn).RegisterDriver(ctx, &driverpb.RegisterDriverRequest{DriverID: "driver-2", PackageSlug: "sedan"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}
