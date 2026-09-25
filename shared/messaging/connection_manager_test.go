package messaging

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gorilla/websocket"
)

func TestWebSocketOriginAllowlist(t *testing.T) {
	manager := NewConnectionManager("https://app.heygo.test")
	allowed := httptest.NewRequest("GET", "http://api.heygo.test/ws", nil)
	allowed.Header.Set("Origin", "https://app.heygo.test")
	if !manager.upgrader.CheckOrigin(allowed) {
		t.Fatal("allowed WebSocket origin rejected")
	}
	blocked := httptest.NewRequest("GET", "http://api.heygo.test/ws", nil)
	blocked.Header.Set("Origin", "https://attacker.test")
	if manager.upgrader.CheckOrigin(blocked) {
		t.Fatal("untrusted WebSocket origin accepted")
	}
}

func TestWebSocketUpgradeAndDelivery(t *testing.T) {
	manager := NewConnectionManager("https://app.heygo.test")
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := manager.Upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		manager.Add("rider-1", conn)
		defer manager.Remove("rider-1")
		close(ready)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	header := http.Header{"Origin": []string{"https://app.heygo.test"}}
	client, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer client.Close()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("websocket was not registered")
	}

	want := contracts.WSMessage{Type: contracts.TripEventStarted, Data: map[string]any{"tripID": "trip-1"}}
	if err := manager.SendMessage("rider-1", want); err != nil {
		t.Fatalf("send websocket message: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var got contracts.WSMessage
	if err := client.ReadJSON(&got); err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	if got.Type != want.Type {
		t.Fatalf("got message type %q, want %q", got.Type, want.Type)
	}
}
