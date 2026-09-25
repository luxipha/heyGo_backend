package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestR2PresignedPut(t *testing.T) {
	store, err := NewR2Store(R2Config{Endpoint: "https://example.r2.cloudflarestorage.com", Bucket: "private-docs", AccessKey: "test-key", SecretKey: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	signed, headers, err := store.PresignPut(context.Background(), "driver-documents/driver/file", "application/pdf", 1234, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "example.r2.cloudflarestorage.com" || !strings.Contains(parsed.Path, "/private-docs/driver-documents/driver/file") {
		t.Fatalf("unexpected URL: %s", signed)
	}
	if headers.Get("Content-Type") != "application/pdf" {
		t.Fatalf("content type was not signed: %v", headers)
	}
	if !strings.Contains(parsed.Query().Get("X-Amz-SignedHeaders"), "content-type") {
		t.Fatalf("content type not in signed headers: %s", signed)
	}
}
