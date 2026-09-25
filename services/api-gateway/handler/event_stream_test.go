package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/gorilla/websocket"
)

type testEventLog struct {
	mu     sync.Mutex
	events []contracts.WSMessage
}

func (s *testEventLog) append(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, contracts.WSMessage{ID: fmt.Sprintf("evt_%d", len(s.events)+1), Type: kind})
}

func (s *testEventLog) LatestCursor(_ context.Context, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("evt_%d", len(s.events)), nil
}

func (s *testEventLog) ReadAfter(_ context.Context, _ string, after string, limit int) ([]contracts.WSMessage, error) {
	id, err := strconv.Atoi(strings.TrimPrefix(after, "evt_"))
	if err != nil || !strings.HasPrefix(after, "evt_") {
		return nil, messaging.ErrInvalidEventCursor
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id >= len(s.events) {
		return nil, nil
	}
	end := min(id+limit, len(s.events))
	return append([]contracts.WSMessage(nil), s.events[id:end]...), nil
}

func TestEventStreamReplaysAndDeliversAcrossGatewayInstances(t *testing.T) {
	log := &testEventLog{}
	log.append("first")
	log.append("second")
	clients := make([]*websocket.Conn, 0, 2)
	for range 2 {
		manager := messaging.NewConnectionManager("https://app.heygo.test")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := manager.Upgrade(w, r)
			if err != nil {
				return
			}
			defer conn.Close()
			cancel, err := attachEventStream(r.Context(), manager, conn, log, "driver-1", "evt_0")
			if err != nil {
				return
			}
			defer cancel()
			defer manager.RemoveIf("driver-1", conn)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}))
		t.Cleanup(server.Close)
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Origin": []string{"https://app.heygo.test"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		clients = append(clients, client)
	}
	log.append("third")
	for _, client := range clients {
		_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
		for i, kind := range []string{"first", "second", "third"} {
			var event contracts.WSMessage
			if err := client.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			if event.ID != fmt.Sprintf("evt_%d", i+1) || event.Type != kind {
				t.Fatalf("event %d = %#v, want %s", i, event, kind)
			}
		}
	}
}
