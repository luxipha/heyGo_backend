package messaging

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gorilla/websocket"
)

var ErrConnectionNotFound = fmt.Errorf("connection not found")

type connWrapper struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type ConnectionManager struct {
	connections map[string]*connWrapper
	mu          sync.RWMutex
	upgrader    websocket.Upgrader
}

func NewConnectionManager(allowedOrigins ...string) *ConnectionManager {
	origins := make([]string, 0, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if value := strings.TrimSpace(origin); value != "" {
			origins = append(origins, strings.TrimSuffix(value, "/"))
		}
	}
	return &ConnectionManager{
		connections: make(map[string]*connWrapper),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
			if origin == "" {
				return false
			}
			if parsed, err := url.Parse(origin); err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return false
			}
			return slices.Contains(origins, origin)
		}},
	}
}

func (cm *ConnectionManager) Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := cm.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (cm *ConnectionManager) Add(id string, conn *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.connections[id] = &connWrapper{
		conn: conn,
		mu:   sync.Mutex{},
	}

	log.Printf("Connection added for ID: %s", id)
}

// AddWithReplay publishes the connection before loading events, but holds its
// write lock until replay completes so live events cannot overtake replay.
func (cm *ConnectionManager) AddWithReplay(id string, conn *websocket.Conn, load func() ([]contracts.WSMessage, error)) error {
	wrapper := &connWrapper{conn: conn}
	wrapper.mu.Lock()
	cm.mu.Lock()
	previous := cm.connections[id]
	cm.connections[id] = wrapper
	cm.mu.Unlock()
	if previous != nil && previous.conn != conn {
		_ = previous.conn.Close()
	}
	events, err := load()
	if err == nil {
		for _, event := range events {
			if err = conn.WriteJSON(event); err != nil {
				break
			}
		}
	}
	if err != nil {
		cm.RemoveIf(id, conn)
	}
	wrapper.mu.Unlock()
	return err
}

func (cm *ConnectionManager) RemoveIf(id string, conn *websocket.Conn) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if wrapper, exists := cm.connections[id]; exists && wrapper.conn == conn {
		delete(cm.connections, id)
		return true
	}
	return false
}

func (cm *ConnectionManager) Remove(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.connections, id)
	log.Printf("Connection removed for ID: %s", id)
}

func (cm *ConnectionManager) Get(id string) (*websocket.Conn, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	wrapper, exists := cm.connections[id]
	if !exists {
		return nil, false
	}
	return wrapper.conn, true
}

func (cm *ConnectionManager) SendMessage(id string, message contracts.WSMessage) error {
	cm.mu.RLock()
	wrapper, exists := cm.connections[id]
	cm.mu.RUnlock()
	if !exists {
		return ErrConnectionNotFound
	}
	wrapper.mu.Lock()
	defer wrapper.mu.Unlock()

	return wrapper.conn.WriteJSON(message)
}

// SendMessageIf avoids an old stream writing to a replacement connection.
func (cm *ConnectionManager) SendMessageIf(id string, conn *websocket.Conn, message contracts.WSMessage) error {
	cm.mu.RLock()
	wrapper, exists := cm.connections[id]
	cm.mu.RUnlock()
	if !exists || wrapper.conn != conn {
		return ErrConnectionNotFound
	}
	wrapper.mu.Lock()
	defer wrapper.mu.Unlock()
	cm.mu.RLock()
	current := cm.connections[id]
	cm.mu.RUnlock()
	if current != wrapper {
		return ErrConnectionNotFound
	}
	return conn.WriteJSON(message)
}
