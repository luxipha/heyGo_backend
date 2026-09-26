package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestGRPCTransportOptionsUsesLocalTransportWithoutAudience(t *testing.T) {
	options, err := GRPCTransportOptions(context.Background(), "")
	if err != nil {
		t.Fatalf("GRPCTransportOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("GRPCTransportOptions() returned %d options, want 1", len(options))
	}
}

func TestAddCloudRunIdentityLeavesLocalRequestUnchanged(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://payment-service:9200/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddCloudRunIdentity(req, ""); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Serverless-Authorization"); got != "" {
		t.Fatalf("unexpected identity header: %q", got)
	}
}

func TestGRPCTransportOptionsRejectsInvalidCloudRunAudience(t *testing.T) {
	for _, audience := range []string{"http://driver.example", "driver.example", "https://driver.example/path"} {
		if _, err := GRPCTransportOptions(context.Background(), audience); err == nil {
			t.Fatalf("GRPCTransportOptions(%q) succeeded, want error", audience)
		}
	}
}
