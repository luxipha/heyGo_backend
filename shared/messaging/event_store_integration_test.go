package messaging

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/google/uuid"
)

func TestEventCursorCannotPassAnUncommittedEarlierEvent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "event-"+id, "event-"+id); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT, 0))`, id); err != nil {
		t.Fatal(err)
	}
	var firstID int64
	if err := tx.QueryRow(ctx, `INSERT INTO user_events(recipient_id,source_event_id,type,data) VALUES($1::UUID,'first','test', '{}'::JSONB) RETURNING id`, id).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := NewEventStore(pool).Append(ctx, id, "second", "test", json.RawMessage(`{}`))
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("second event passed uncommitted first event: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	events, err := NewEventStore(pool).ReadAfter(ctx, id, "evt_0", 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	got, err := strconv.ParseInt(strings.TrimPrefix(events[1].ID, "evt_"), 10, 64)
	if err != nil || got <= firstID {
		t.Fatalf("cursor order: first=%d second=%s err=%v", firstID, events[1].ID, err)
	}
}
