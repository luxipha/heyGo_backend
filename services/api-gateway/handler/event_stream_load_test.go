package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type measuredEventReader struct {
	store     *messaging.EventStore
	reads     atomic.Int64
	latencies chan time.Duration
}

func (r *measuredEventReader) LatestCursor(ctx context.Context, id string) (string, error) {
	return r.store.LatestCursor(ctx, id)
}

func (r *measuredEventReader) ReadAfter(ctx context.Context, id, after string, limit int) ([]contracts.WSMessage, error) {
	start := time.Now()
	events, err := r.store.ReadAfter(ctx, id, after, limit)
	r.reads.Add(1)
	select {
	case r.latencies <- time.Since(start):
	default:
	}
	return events, err
}

func (r *measuredEventReader) ReadAfterSession(ctx context.Context, id, sessionID, after string, limit int) ([]contracts.WSMessage, bool, error) {
	start := time.Now()
	events, ownsSession, err := r.store.ReadAfterSession(ctx, id, sessionID, after, limit)
	r.reads.Add(1)
	select {
	case r.latencies <- time.Since(start):
	default:
	}
	return events, ownsSession, err
}

// RUN_POLL_LOAD_TEST=1 TEST_DATABASE_URL=postgres://... go test ./services/api-gateway/handler -run TestSocketPollingLoad -v
// Measures actual Postgres queries from 250 ms polling across two gateway
// instances. It intentionally makes no production capacity assertion.
func TestSocketPollingLoad(t *testing.T) {
	if os.Getenv("RUN_POLL_LOAD_TEST") != "1" {
		t.Skip("set RUN_POLL_LOAD_TEST=1")
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	clients := 100
	if raw := os.Getenv("POLL_LOAD_CLIENTS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 2000 {
			t.Fatal("POLL_LOAD_CLIENTS must be 1..2000")
		}
		clients = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reader := &measuredEventReader{store: messaging.NewEventStore(pool), latencies: make(chan time.Duration, clients*30)}
	servers := make([]*httptest.Server, 2)
	for i := range servers {
		manager := messaging.NewConnectionManager("https://app.heygo.test")
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			conn, err := manager.Upgrade(w, req)
			if err != nil {
				return
			}
			defer conn.Close()
			id := req.URL.Query().Get("id")
			sessionID := req.URL.Query().Get("session")
			stop, err := attachEventStreamForSession(req.Context(), manager, conn, reader, id, sessionID, "evt_0")
			if err != nil {
				return
			}
			defer stop()
			defer manager.RemoveIf(id, conn)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}))
		t.Cleanup(servers[i].Close)
	}
	connections := make([]*websocket.Conn, 0, clients)
	for i := 0; i < clients; i++ {
		driverID, sessionID := uuid.NewString(), uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "load-test-"+driverID, "load-test-"+driverID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO driver_socket_sessions(driver_id,session_id,expires_at) VALUES($1::UUID,$2::UUID,NOW()+INTERVAL '1 minute')`, driverID, sessionID); err != nil {
			t.Fatal(err)
		}
		endpoint := "ws" + strings.TrimPrefix(servers[i%2].URL, "http") + "?id=" + driverID + "&session=" + sessionID
		conn, _, err := websocket.DefaultDialer.Dial(endpoint, http.Header{"Origin": []string{"https://app.heygo.test"}})
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	start := time.Now()
	initial := reader.reads.Load()
	time.Sleep(5 * time.Second)
	count := reader.reads.Load() - initial
	for _, conn := range connections {
		_ = conn.Close()
	}
	values := make([]time.Duration, 0, len(reader.latencies))
	for len(reader.latencies) > 0 {
		values = append(values, <-reader.latencies)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	p95 := time.Duration(0)
	if len(values) > 0 {
		p95 = values[(len(values)-1)*95/100]
	}
	qps := float64(count) / time.Since(start).Seconds()
	t.Logf("connections=%d replicas=2 read_queries=%d qps=%.1f reads_per_socket_per_second=%.2f p95_read_latency=%s", clients, count, qps, qps/float64(clients), p95)
	if count < int64(clients) {
		t.Fatal(fmt.Sprintf("polling stalled: %d reads for %d sockets", count, clients))
	}
}
