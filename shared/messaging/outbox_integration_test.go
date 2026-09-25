package messaging

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type outboxTestPublisher struct {
	ids   []string
	after func()
}

func TestOutboxKafkaGatewayReplayDeduplicates(t *testing.T) {
	url, brokers := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_KAFKA_BROKERS")
	if url == "" || brokers == "" {
		t.Skip("TEST_DATABASE_URL and TEST_KAFKA_BROKERS are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	drainPendingTestOutbox(t, ctx, pool)
	riderID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = pool.Exec(cleanup, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1::UUID`, riderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, riderID, "kafka-"+riderID, "kafka-"+riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',1000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'started')`, tripID, riderID, fareID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"trip": map[string]any{"id": tripID}})
	var outboxID int64
	if err := pool.QueryRow(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload) VALUES($1::UUID,$2,$3::UUID,$4::JSONB) RETURNING id`, tripID, contracts.TripEventStarted, riderID, payload).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	kfc, err := kafka.NewKafkaClient(strings.Split(brokers, ","), "heygo-outbox-test-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kfc.Close)
	if err := kfc.EnsureTopics(ctx, strings.Split(brokers, ","), []string{contracts.TripEventStarted}, 1, 1); err != nil {
		t.Fatal(err)
	}
	consumerCtx, stopConsumer := context.WithCancel(ctx)
	t.Cleanup(stopConsumer)
	go func() {
		_ = NewTopicConsumer(kfc, nil, []string{contracts.TripEventStarted}, NewEventStore(pool)).Consume(consumerCtx)
	}()
	if published, err := publishNext(ctx, pool, kfc.Producer); err != nil || !published {
		t.Fatalf("initial Kafka publish: published=%v err=%v", published, err)
	}
	waitForEventCount(t, ctx, pool, riderID, 1)
	if _, err := pool.Exec(ctx, `UPDATE trip_event_outbox SET published_at=NULL WHERE id=$1`, outboxID); err != nil {
		t.Fatal(err)
	}
	if published, err := publishNext(ctx, pool, kfc.Producer); err != nil || !published {
		t.Fatalf("Kafka replay: published=%v err=%v", published, err)
	}
	time.Sleep(750 * time.Millisecond)
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID`, riderID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replayed Kafka event created %d user events; want 1", count)
	}
}

func TestOfferExpiryAndReassignmentCommitWithEvents(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, first, second := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fare, trip := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM trips WHERE id=$1::UUID`, trip)
		_, _ = pool.Exec(cleanup, `DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
		_, _ = pool.Exec(cleanup, `DELETE FROM drivers WHERE id IN ($1::UUID,$2::UUID)`, first, second)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID,$3::UUID)`, rider, first, second)
	})
	for _, user := range []struct{ id, role string }{{rider, "rider"}, {first, "driver"}, {second, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, user.id, "assignment-"+user.id, "assignment-"+user.id, user.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{first, second} {
		if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Test Driver','TEST-123','sedan','available')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',1000,'{}'::JSONB)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, trip, rider, fare); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"trip": map[string]any{"id": trip}})
	if _, err := pool.Exec(ctx, `INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,trip_payload) VALUES($1::UUID,$2::UUID,1,'offered',NOW()+INTERVAL '1 minute',$3::JSONB)`, trip, first, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_assignments SET status='timed_out' WHERE trip_id=$1::UUID AND attempt=1`, trip); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_event_outbox WHERE trip_id=$1::UUID`, trip).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back expiry left %d events: %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE driver_assignments SET status='timed_out' WHERE trip_id=$1::UUID AND attempt=1`, trip); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,trip_payload) VALUES($1::UUID,$2::UUID,2,'offered',NOW()+INTERVAL '1 minute',$3::JSONB)`, trip, second, payload); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT topic,recipient_id::TEXT FROM trip_event_outbox WHERE trip_id=$1::UUID ORDER BY id`, trip)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var topic, recipient string
		if err := rows.Scan(&topic, &recipient); err != nil {
			t.Fatal(err)
		}
		got[topic+":"+recipient] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"trip.event.expired:" + first, "trip.event.driver_not_interested:" + rider, "trip.event.reassigned:" + first, "trip.event.reassigned:" + rider} {
		if !got[key] {
			t.Fatalf("missing durable offer event %s in %v", key, got)
		}
	}
}

func waitForEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, recipient string, want int) {
	t.Helper()
	for ctx.Err() == nil {
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID`, recipient).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d user events: %v", want, ctx.Err())
}

func (p *outboxTestPublisher) SendMessageAndWait(_ context.Context, _ string, message *contracts.KafkaMessage, _ time.Duration) error {
	p.ids = append(p.ids, message.EventID)
	if p.after != nil {
		p.after()
	}
	return nil
}

func TestOutboxRetriesWithStableIDAfterAcknowledgedPublishAndFailedCommit(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	drainPendingTestOutbox(t, ctx, pool)
	riderID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = pool.Exec(cleanup, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1::UUID`, riderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, riderID, "outbox-"+riderID, "outbox-"+riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',1000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'started')`, tripID, riderID, fareID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"trip": map[string]any{"id": tripID}})
	var outboxID int64
	if err := pool.QueryRow(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload) VALUES($1::UUID,'trip.event.started',$2::UUID,$3::JSONB) RETURNING id`, tripID, riderID, payload).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}

	firstCtx, stopFirst := context.WithCancel(ctx)
	first := &outboxTestPublisher{after: stopFirst}
	if published, err := publishNext(firstCtx, pool, first); err == nil || published {
		t.Fatalf("acknowledged publish must remain pending after failed DB commit: published=%v err=%v", published, err)
	}
	var pending bool
	if err := pool.QueryRow(ctx, `SELECT published_at IS NULL FROM trip_event_outbox WHERE id=$1`, outboxID).Scan(&pending); err != nil || !pending {
		t.Fatalf("outbox row lost: pending=%v err=%v", pending, err)
	}
	second := &outboxTestPublisher{}
	if published, err := publishNext(ctx, pool, second); err != nil || !published {
		t.Fatalf("retry failed: published=%v err=%v", published, err)
	}
	if len(first.ids) != 1 || len(second.ids) != 1 || first.ids[0] != second.ids[0] {
		t.Fatalf("event IDs changed across retry: %v %v", first.ids, second.ids)
	}
	if published, err := publishNext(ctx, pool, second); err != nil || published {
		t.Fatalf("published row should not replay: published=%v err=%v", published, err)
	}
}

// The test database is disposable and publishNext intentionally drains the
// global outbox. Mark fixtures left by earlier integration tests as drained so
// this test exercises only its own event.
func drainPendingTestOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE trip_event_outbox SET published_at=NOW() WHERE published_at IS NULL`); err != nil {
		t.Fatalf("isolate outbox test rows: %v", err)
	}
}
