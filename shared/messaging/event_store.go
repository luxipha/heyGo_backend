package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type EventStore struct {
	pool     *pgxpool.Pool
	notifier *EventNotifier
}

var ErrInvalidEventCursor = errors.New("invalid event cursor")

func NewEventStore(pool *pgxpool.Pool, notifiers ...*EventNotifier) *EventStore {
	var notifier *EventNotifier
	if len(notifiers) > 0 {
		notifier = notifiers[0]
	}
	return &EventStore{pool: pool, notifier: notifier}
}

func (s *EventStore) SubscribeEventWake(recipientID string) (<-chan struct{}, func()) {
	if s.notifier == nil {
		return nil, func() {}
	}
	return s.notifier.Subscribe(recipientID)
}

// AppendEventTx appends an event with notification fan-out using a caller-owned
// transaction. It is useful to handlers that already own the transaction.
func AppendEventTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID, kind string, data json.RawMessage) (contracts.WSMessage, error) {
	return (&EventStore{}).AppendTx(ctx, tx, recipientID, sourceEventID, kind, data)
}

func AppendNotificationTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID string, notification InboxNotification) error {
	return (&EventStore{}).AppendNotificationTx(ctx, tx, recipientID, sourceEventID, notification)
}

func (s *EventStore) Append(ctx context.Context, recipientID, sourceEventID, kind string, data json.RawMessage) (contracts.WSMessage, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.WSMessage{}, fmt.Errorf("begin user event: %w", err)
	}
	defer tx.Rollback(ctx)
	event, err := s.AppendTx(ctx, tx, recipientID, sourceEventID, kind, data)
	if err != nil {
		return contracts.WSMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.WSMessage{}, fmt.Errorf("commit user event: %w", err)
	}
	return event, nil
}

// AppendTx appends to one user's ordered durable stream within a caller-owned
// transaction. Use this when an event must commit atomically with its source row.
func (s *EventStore) AppendTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID, kind string, data json.RawMessage) (contracts.WSMessage, error) {
	// Serialize writers for one recipient before allocating the identity ID.
	// Otherwise a later ID can commit first and a cursor can skip the earlier one.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT, 0))`, recipientID); err != nil {
		return contracts.WSMessage{}, fmt.Errorf("lock user event stream: %w", err)
	}
	event, err := appendUserEventTx(ctx, tx, recipientID, sourceEventID, kind, data)
	if err != nil {
		return contracts.WSMessage{}, err
	}
	if notification := notificationForDriverEvent(kind, data); notification != nil {
		var isDriver bool
		if err := tx.QueryRow(ctx, `SELECT roles @> ARRAY['driver']::TEXT[] FROM users WHERE id=$1::UUID`, recipientID).Scan(&isDriver); err != nil {
			return contracts.WSMessage{}, fmt.Errorf("check notification recipient role: %w", err)
		}
		if isDriver {
			if err := s.createNotificationTx(ctx, tx, recipientID, sourceEventID, *notification); err != nil {
				return contracts.WSMessage{}, err
			}
		}
	}
	return event, nil
}

// AppendNotificationTx adds an explicitly composed inbox notification and its
// socket event atomically with the caller's business transaction.
func (s *EventStore) AppendNotificationTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID string, notification InboxNotification) error {
	if strings.TrimSpace(recipientID) == "" || strings.TrimSpace(sourceEventID) == "" || strings.TrimSpace(notification.Type) == "" || strings.TrimSpace(notification.Title) == "" {
		return errors.New("notification recipient, source, type, and title are required")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT, 0))`, recipientID); err != nil {
		return fmt.Errorf("lock user notification stream: %w", err)
	}
	return s.createNotificationTx(ctx, tx, recipientID, sourceEventID, notification)
}

func (s *EventStore) createNotificationTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID string, notification InboxNotification) error {
	data := notification.Data
	if len(data) == 0 || !json.Valid(data) {
		data = json.RawMessage(`{}`)
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO driver_notifications(driver_id,source_event_id,type,title,message,data)
		VALUES($1::UUID,$2,$3,$4,$5,$6::JSONB)
		ON CONFLICT(driver_id,source_event_id) DO UPDATE SET source_event_id=EXCLUDED.source_event_id
		RETURNING id::TEXT`, recipientID, sourceEventID, notification.Type, notification.Title, notification.Message, data).Scan(&id)
	if err != nil {
		return fmt.Errorf("insert driver notification: %w", err)
	}
	eventData, err := json.Marshal(map[string]any{"notification": map[string]any{
		"id": id, "type": notification.Type, "title": notification.Title, "message": notification.Message, "data": data,
	}})
	if err != nil {
		return fmt.Errorf("encode driver notification event: %w", err)
	}
	if _, err := appendUserEventTx(ctx, tx, recipientID, sourceEventID+":notification.created", "notification.created", eventData); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_notification_push_deliveries(notification_id,device_id)
		SELECT $1::BIGINT,id FROM driver_devices WHERE driver_id=$2::UUID ON CONFLICT(notification_id,device_id) DO NOTHING`, id, recipientID); err != nil {
		return fmt.Errorf("queue driver notification push: %w", err)
	}
	return nil
}

func appendUserEventTx(ctx context.Context, tx pgx.Tx, recipientID, sourceEventID, kind string, data json.RawMessage) (contracts.WSMessage, error) {
	var id int64
	var occurredAt time.Time
	err := tx.QueryRow(ctx, `INSERT INTO user_events(recipient_id,source_event_id,type,data)
		VALUES($1::UUID,$2,$3,$4::JSONB) ON CONFLICT(recipient_id,source_event_id,type)
		DO UPDATE SET source_event_id=EXCLUDED.source_event_id RETURNING id,occurred_at`, recipientID, sourceEventID, kind, data).Scan(&id, &occurredAt)
	if err != nil {
		return contracts.WSMessage{}, fmt.Errorf("append user event: %w", err)
	}
	return contracts.WSMessage{ID: fmt.Sprintf("evt_%d", id), Type: kind, OccurredAt: occurredAt, Data: data}, nil
}

// LatestCursor starts a fresh socket at the current tail without replaying old events.
func (s *EventStore) LatestCursor(ctx context.Context, recipientID string) (string, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id),0) FROM user_events WHERE recipient_id=$1::UUID`, recipientID).Scan(&id); err != nil {
		return "", err
	}
	return fmt.Sprintf("evt_%d", id), nil
}

func (s *EventStore) ReadAfter(ctx context.Context, recipientID, after string, limit int) ([]contracts.WSMessage, error) {
	var afterID int64
	if after != "" {
		var err error
		afterID, err = strconv.ParseInt(strings.TrimPrefix(after, "evt_"), 10, 64)
		if err != nil || afterID < 0 || !strings.HasPrefix(after, "evt_") {
			return nil, ErrInvalidEventCursor
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id,type,data,occurred_at FROM user_events WHERE recipient_id=$1::UUID AND id>$2 ORDER BY id LIMIT $3`, recipientID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.WSMessage, 0)
	for rows.Next() {
		var id int64
		var kind string
		var data json.RawMessage
		var occurredAt time.Time
		if err := rows.Scan(&id, &kind, &data, &occurredAt); err != nil {
			return nil, err
		}
		result = append(result, contracts.WSMessage{ID: fmt.Sprintf("evt_%d", id), Type: kind, OccurredAt: occurredAt, Data: data})
	}
	return result, rows.Err()
}

// ReadAfterSession reads events only while the caller still owns the driver's
// shared socket lease. Checking the lease and reading the event batch in one
// query prevents a replaced gateway from delivering queued events after the
// next poll, without adding a second database round trip per socket.
func (s *EventStore) ReadAfterSession(ctx context.Context, recipientID, sessionID, after string, limit int) ([]contracts.WSMessage, bool, error) {
	var afterID int64
	if after != "" {
		var err error
		afterID, err = strconv.ParseInt(strings.TrimPrefix(after, "evt_"), 10, 64)
		if err != nil || afterID < 0 || !strings.HasPrefix(after, "evt_") {
			return nil, false, ErrInvalidEventCursor
		}
	}
	var ownsSession bool
	var rawEvents []byte
	err := s.pool.QueryRow(ctx, `
		WITH owner AS (
			SELECT EXISTS (
				SELECT 1 FROM driver_socket_sessions
				WHERE driver_id=$1::UUID AND session_id=$2::UUID AND expires_at>NOW()
			) AS owns
		), events AS (
			SELECT id,type,data,occurred_at FROM user_events
			WHERE recipient_id=$1::UUID AND id>$3
			ORDER BY id LIMIT $4
		)
		SELECT owner.owns,
			COALESCE(jsonb_agg(jsonb_build_object(
				'id','evt_'||events.id::TEXT,
				'type',events.type,
				'data',events.data,
				'occurredAt',events.occurred_at
			) ORDER BY events.id) FILTER (WHERE events.id IS NOT NULL), '[]'::JSONB)
		FROM owner LEFT JOIN events ON owner.owns
		GROUP BY owner.owns`, recipientID, sessionID, afterID, limit).Scan(&ownsSession, &rawEvents)
	if err != nil {
		return nil, false, fmt.Errorf("read events for socket session: %w", err)
	}
	if !ownsSession {
		return nil, false, nil
	}
	var events []contracts.WSMessage
	if err := json.Unmarshal(rawEvents, &events); err != nil {
		return nil, true, fmt.Errorf("decode socket events: %w", err)
	}
	return events, true, nil
}
