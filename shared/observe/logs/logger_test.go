package logs

import "testing"

func TestLoadConfigDefaultsToStdoutOnly(t *testing.T) {
	t.Setenv("LOG_FILE", "")

	if got := loadConfig("test-service").LogFile; got != "" {
		t.Fatalf("LogFile = %q, want stdout only", got)
	}
}

func TestLoadConfigAcceptsExplicitLogFile(t *testing.T) {
	t.Setenv("LOG_FILE", "/tmp/test-service.log")

	if got := loadConfig("test-service").LogFile; got != "/tmp/test-service.log" {
		t.Fatalf("LogFile = %q, want explicit path", got)
	}
}
