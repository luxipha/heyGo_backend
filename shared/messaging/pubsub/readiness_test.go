package pubsub

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pingServer struct {
	pubsubpb.UnimplementedPublisherServer
	iampb.UnimplementedIAMPolicyServer
	missing, permitted   bool
	topicCalls, iamCalls atomic.Int32
}

func (s *pingServer) GetTopic(_ context.Context, r *pubsubpb.GetTopicRequest) (*pubsubpb.Topic, error) {
	s.topicCalls.Add(1)
	if r.Topic != "projects/local-readiness-test/topics/"+contracts.AllTopics()[0] {
		return nil, status.Error(codes.InvalidArgument, "unexpected topic")
	}
	if s.missing {
		return nil, status.Error(codes.NotFound, "topic not provisioned")
	}
	return &pubsubpb.Topic{Name: r.Topic}, nil
}
func (s *pingServer) TestIamPermissions(_ context.Context, r *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	s.iamCalls.Add(1)
	if r.Resource != "projects/local-readiness-test/topics/"+contracts.AllTopics()[0] || len(r.Permissions) != 1 || r.Permissions[0] != "pubsub.topics.publish" {
		return nil, status.Error(codes.InvalidArgument, "unexpected permission probe")
	}
	if s.permitted {
		return &iampb.TestIamPermissionsResponse{Permissions: r.Permissions}, nil
	}
	return &iampb.TestIamPermissionsResponse{}, nil
}
func TestPingEmulatorAndCloudReadiness(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		emulator, missing, permitted, wantError bool
	}{
		{name: "emulator provisioned topic", emulator: true},
		{name: "emulator missing topic", emulator: true, missing: true, wantError: true},
		{name: "cloud publisher permission", permitted: true},
		{name: "cloud missing publisher permission", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			stub := &pingServer{missing: tc.missing, permitted: tc.permitted}
			server := grpc.NewServer()
			pubsubpb.RegisterPublisherServer(server, stub)
			iampb.RegisterIAMPolicyServer(server, stub)
			go server.Serve(listener)
			t.Cleanup(server.Stop)
			// Route both branch tests to a local mock; never use cloud credentials.
			t.Setenv("PUBSUB_EMULATOR_HOST", listener.Addr().String())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := NewClient(ctx, "local-readiness-test", "local-group")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			if !client.emulator {
				t.Fatal("emulator environment was not detected")
			}
			client.emulator = tc.emulator
			err = client.Ping(ctx)
			if (err != nil) != tc.wantError {
				t.Fatalf("Ping error=%v, wantError=%v", err, tc.wantError)
			}
			if tc.emulator {
				if stub.topicCalls.Load() != 1 || stub.iamCalls.Load() != 0 {
					t.Fatal("emulator readiness used unsupported IAM")
				}
			} else if stub.iamCalls.Load() != 1 || stub.topicCalls.Load() != 0 {
				t.Fatal("cloud readiness skipped IAM validation")
			}
		})
	}
}
