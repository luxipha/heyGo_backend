package env

import "testing"

func TestListenAddrUsesCloudRunPort(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("HTTP_ADDR", ":9999")

	if got := ListenAddr("HTTP_ADDR", ":3000"); got != ":8080" {
		t.Fatalf("ListenAddr() = %q, want %q", got, ":8080")
	}
}

func TestListenAddrFallsBackToServiceAddress(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("GRPC_ADDR", ":9100")

	if got := ListenAddr("GRPC_ADDR", ":9000"); got != ":9100" {
		t.Fatalf("ListenAddr() = %q, want %q", got, ":9100")
	}
}

func TestListenAddrFallsBackToDefault(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("PAYMENT_HTTP_ADDR", "")

	if got := ListenAddr("PAYMENT_HTTP_ADDR", ":9200"); got != ":9200" {
		t.Fatalf("ListenAddr() = %q, want %q", got, ":9200")
	}
}
