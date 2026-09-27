package messaging

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const userEventNotificationChannel = "heygo_user_events"

// EventNotifier keeps one PostgreSQL LISTEN connection per gateway instance
// and fans committed user-event notifications out to local socket streams.
// Socket streams still perform periodic reads so a transient listener outage
// cannot lose an event permanently.
type EventNotifier struct {
	pool *pgxpool.Pool

	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func NewEventNotifier(pool *pgxpool.Pool) *EventNotifier {
	return &EventNotifier{pool: pool, subscribers: make(map[string]map[chan struct{}]struct{})}
}

func (n *EventNotifier) Subscribe(recipientID string) (<-chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subscribers[recipientID] == nil {
		n.subscribers[recipientID] = make(map[chan struct{}]struct{})
	}
	n.subscribers[recipientID][wake] = struct{}{}
	n.mu.Unlock()

	var once sync.Once
	return wake, func() {
		once.Do(func() {
			n.mu.Lock()
			delete(n.subscribers[recipientID], wake)
			if len(n.subscribers[recipientID]) == 0 {
				delete(n.subscribers, recipientID)
			}
			n.mu.Unlock()
		})
	}
}

func (n *EventNotifier) Run(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := n.pool.Acquire(ctx)
		if err != nil {
			n.waitBeforeReconnect(ctx, err)
			continue
		}
		if _, err = conn.Exec(ctx, "LISTEN "+userEventNotificationChannel); err == nil {
			for ctx.Err() == nil {
				notification, waitErr := conn.Conn().WaitForNotification(ctx)
				if waitErr != nil {
					err = waitErr
					break
				}
				n.notify(notification.Payload)
			}
		}
		conn.Release()
		if ctx.Err() == nil {
			n.waitBeforeReconnect(ctx, err)
		}
	}
}

func (n *EventNotifier) notify(recipientID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for wake := range n.subscribers[recipientID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (n *EventNotifier) waitBeforeReconnect(ctx context.Context, err error) {
	if err != nil {
		log.Printf("user event listener reconnecting: %v", err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
