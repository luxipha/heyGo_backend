package repo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

// Declining an offer that is no longer offered (expired, accepted elsewhere or
// already declined) must report ErrAssignmentNotActive so the consumer can
// acknowledge the message instead of retrying it forever.
func TestDeclineAssignmentReportsResolvedAssignmentAsNotActive(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := &postgresDriverRepo{pool: pool}

	_, err = repository.DeclineAssignment(ctx, uuid.NewString(), uuid.NewString())

	if !errors.Is(err, ErrAssignmentNotActive) {
		t.Fatalf("expected ErrAssignmentNotActive, got %v", err)
	}
}
