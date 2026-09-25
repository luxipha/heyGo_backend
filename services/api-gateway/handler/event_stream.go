package handler

import (
	"context"
	"errors"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/gorilla/websocket"
)

const eventPollInterval = 250 * time.Millisecond
const eventBatchSize = 100

type eventReader interface {
	LatestCursor(context.Context, string) (string, error)
	ReadAfter(context.Context, string, string, int) ([]contracts.WSMessage, error)
}

type sessionEventReader interface {
	ReadAfterSession(context.Context, string, string, string, int) ([]contracts.WSMessage, bool, error)
}

var errSocketSessionSuperseded = errors.New("driver socket session was superseded")

// attachEventStream reads the common database log, so the WebSocket can be
// hosted on any gateway replica regardless of which one consumed Kafka.
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
	go func() {
		ticker := time.NewTicker(eventPollInterval)
		defer ticker.Stop()
		for {
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
					continue
				}
			}
			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel, nil
}
