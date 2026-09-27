package handler

import (
	"context"
	"errors"
	"time"

	"github.com/gorilla/websocket"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

const (
	eventPollFallbackInterval     = 2 * time.Second
	eventNotifierRecoveryInterval = 15 * time.Second
)
const eventBatchSize = 100

type eventReader interface {
	LatestCursor(context.Context, string) (string, error)
	ReadAfter(context.Context, string, string, int) ([]contracts.WSMessage, error)
}

type sessionEventReader interface {
	ReadAfterSession(context.Context, string, string, string, int) ([]contracts.WSMessage, bool, error)
}

type eventWakeSubscriber interface {
	SubscribeEventWake(string) (<-chan struct{}, func())
}

var errSocketSessionSuperseded = errors.New("driver socket session was superseded")

// attachEventStream reads the common database log, so the WebSocket can be
// hosted on any gateway replica regardless of which one consumed Pub/Sub.
func attachEventStream(ctx context.Context, manager *messaging.ConnectionManager, conn *websocket.Conn, store eventReader, recipientID, after string) (context.CancelFunc, error) {
	return attachEventStreamForSession(ctx, manager, conn, store, recipientID, "", after)
}

func attachEventStreamForSession(ctx context.Context, manager *messaging.ConnectionManager, conn *websocket.Conn, store eventReader, recipientID, sessionID, after string) (context.CancelFunc, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	if after == "" {
		var err error
		after, err = store.LatestCursor(streamCtx, recipientID)
		if err != nil {
			cancel()
			return nil, err
		}
	}
	cursor := after
	readAfter := func(ctx context.Context, after string) ([]contracts.WSMessage, error) {
		if sessionID == "" {
			return store.ReadAfter(ctx, recipientID, after, eventBatchSize)
		}
		sessionStore, ok := store.(sessionEventReader)
		if !ok {
			return nil, errors.New("event store does not support driver session checks")
		}
		events, ownsSession, err := sessionStore.ReadAfterSession(ctx, recipientID, sessionID, after, eventBatchSize)
		if err != nil {
			return nil, err
		}
		if !ownsSession {
			return nil, errSocketSessionSuperseded
		}
		return events, nil
	}
	err := manager.AddWithReplay(recipientID, conn, func() ([]contracts.WSMessage, error) {
		events, err := readAfter(streamCtx, cursor)
		if len(events) > 0 {
			cursor = events[len(events)-1].ID
		}
		return events, err
	})
	if err != nil {
		cancel()
		return nil, err
	}
	var wake <-chan struct{}
	unsubscribe := func() {}
	if subscriber, ok := store.(eventWakeSubscriber); ok {
		wake, unsubscribe = subscriber.SubscribeEventWake(recipientID)
	}
	go func() {
		defer unsubscribe()
		fallbackInterval := eventPollFallbackInterval
		if wake != nil {
			fallbackInterval = eventNotifierRecoveryInterval
		}
		ticker := time.NewTicker(fallbackInterval)
		defer ticker.Stop()
		// Read once immediately after subscribing. This closes the window where
		// an event can be committed after replay but before the wake subscription
		// is installed. It also lets a replay larger than one batch drain without
		// waiting for another notification or the recovery ticker.
		readImmediately := true
		for {
			if !readImmediately {
				select {
				case <-streamCtx.Done():
					return
				case <-wake:
				case <-ticker.C:
				}
			}
			readImmediately = false
			events, err := readAfter(streamCtx, cursor)
			if err != nil {
				if streamCtx.Err() != nil {
					return
				}
				if errors.Is(err, errSocketSessionSuperseded) {
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "driver socket replaced"), time.Now().Add(time.Second))
					_ = conn.Close()
					return
				}
				logs.L().Warnw("event stream read failed", "recipientID", recipientID, "error", err)
			} else {
				for _, event := range events {
					if err := manager.SendMessageIf(recipientID, conn, event); err != nil {
						if !errors.Is(err, messaging.ErrConnectionNotFound) {
							logs.L().Warnw("event stream socket write failed", "recipientID", recipientID, "error", err)
						}
						return
					}
					cursor = event.ID
				}
				if len(events) == eventBatchSize {
					readImmediately = true
					continue
				}
			}
		}
	}()
	return cancel, nil
}
