package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// Rider notification data is an allowlisted navigation hint, never event data.
func riderNotificationForEvent(kind string, raw json.RawMessage) *InboxNotification {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	id, _ := p["tripId"].(string)
	if id == "" {
		id, _ = p["id"].(string)
	}
	if t, ok := p["trip"].(map[string]any); ok {
		id, _ = t["id"].(string)
	}
	target, category, title := "trip", "ride_updates", "Ride updated"
	switch kind {
	case contracts.TripEventCreated:
		title = "Ride requested"
	case contracts.TripEventReassigned:
		title = "Finding another driver"
	case contracts.TripEventDriverAssigned:
		title = "Driver assigned"
	case contracts.TripEventArrived:
		title = "Driver arrived"
	case contracts.TripEventStarted:
		title = "Trip started"
	case contracts.TripEventCompleted:
		title = "Trip completed"
		target = "receipt"
	case contracts.TripEventCancelled:
		title = "Trip cancelled"
	case contracts.TripEventNoDriversFound:
		title = "No driver available"
	case contracts.TripEventSettlementUpdated:
		title = "Trip payment status updated"
	case contracts.TripEventNoShowClaimUpdated:
		title = "No-show review updated"
		target = "debt"
		category = "account_alerts"
	case "trip.message.created":
		title = "New trip message"
	case "support.case.updated", "support.message.created":
		id, _ = p["caseId"].(string)
		target = "support_case"
		category = "account_alerts"
		title = "Support updated"
	case "safety.sos.created":
		title = "Safety request received"
		category = "account_alerts"
	default:
		return nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "target": map[string]string{"kind": target, "id": id}})
	return &InboxNotification{Type: category, Title: title, Message: "Open HeyGo to review the latest status.", Data: data}
}

func (s *EventStore) createRiderNotificationTx(ctx context.Context, tx pgx.Tx, recipient, source string, n InboxNotification) error {
	// Serialize account deactivation and preference mutations against fan-out.
	// A notification cannot commit after a reviewed deletion has cleared the inbox.
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active AND roles @> ARRAY['rider'] FROM users WHERE id=$1::UUID FOR SHARE`, recipient).Scan(&active); err != nil {
		return err
	}
	if !active {
		return nil
	}
	var hint struct {
		Target struct{ Kind, ID string } `json:"target"`
	}
	if json.Unmarshal(n.Data, &hint) != nil {
		return fmt.Errorf("invalid Rider notification target")
	}
	// Role alone is insufficient for dual-role users: validate resource ownership.
	var owned bool
	if hint.Target.Kind == "support_case" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM support_cases c JOIN users u ON u.id=c.opened_by WHERE c.id=$1::UUID AND c.opened_by=$2::UUID AND c.audience='rider' AND u.active AND u.roles @> ARRAY['rider'])`, hint.Target.ID, recipient).Scan(&owned); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trips t JOIN users u ON u.id=t.rider_id WHERE t.id=$1::UUID AND t.rider_id=$2::UUID AND u.active AND u.roles @> ARRAY['rider'])`, hint.Target.ID, recipient).Scan(&owned); err != nil {
			return err
		}
	}
	if !owned {
		return nil
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO rider_notifications(rider_id,source_event_id,type,title,message,data) VALUES($1::UUID,$2,$3,$4,$5,$6::JSONB) ON CONFLICT(rider_id,source_event_id) DO UPDATE SET source_event_id=EXCLUDED.source_event_id RETURNING id::TEXT`, recipient, source, n.Type, n.Title, n.Message, n.Data).Scan(&id)
	if err != nil {
		return err
	}
	eventData, _ := json.Marshal(map[string]any{"notificationId": id})
	if _, err = appendUserEventTx(ctx, tx, recipient, source+":rider.notification.created", "rider.notification.created", eventData); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rider_notification_push_deliveries(notification_id,device_id) SELECT $1::BIGINT,d.id FROM rider_devices d JOIN user_sessions session ON session.id=d.session_id LEFT JOIN rider_notification_preferences p ON p.rider_id=d.rider_id WHERE d.rider_id=$2::UUID AND d.enabled AND session.revoked_at IS NULL AND session.refresh_expires_at>NOW() AND CASE $3 WHEN 'ride_updates' THEN COALESCE(p.ride_updates,TRUE) WHEN 'account_alerts' THEN COALESCE(p.account_alerts,TRUE) ELSE COALESCE(p.promotions,FALSE) END ON CONFLICT DO NOTHING`, id, recipient, n.Type)
	return err
}

// AppendRiderBroadcastTx accepts only a pending, Admin-snapshotted recipient.
// Existing trip/support notification ownership checks remain unchanged.
func AppendRiderBroadcastTx(ctx context.Context, tx pgx.Tx, recipient, broadcast string, n InboxNotification) error {
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_push_recipients r JOIN users u ON u.id=r.user_id WHERE r.broadcast_id=$1::UUID AND r.user_id=$2::UUID AND r.audience='rider' AND r.status='pending' AND u.active AND u.roles @> ARRAY['rider'])`, broadcast, recipient).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("broadcast recipient not owned")
	}
	if n.Type != "account_alerts" && n.Type != "promotions" {
		return fmt.Errorf("unsupported broadcast notification type")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT,0))`, recipient); err != nil {
		return err
	}
	source := "broadcast:" + broadcast
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO rider_notifications(rider_id,source_event_id,type,title,message,data) VALUES($1::UUID,$2,$3,$4,$5,$6::JSONB) ON CONFLICT(rider_id,source_event_id) DO UPDATE SET source_event_id=EXCLUDED.source_event_id RETURNING id::TEXT`, recipient, source, n.Type, n.Title, n.Message, n.Data).Scan(&id)
	if err != nil {
		return err
	}
	eventData, _ := json.Marshal(map[string]any{"notificationId": id})
	if _, err = appendUserEventTx(ctx, tx, recipient, source+":rider.notification.created", "rider.notification.created", eventData); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rider_notification_push_deliveries(notification_id,device_id) SELECT $1::BIGINT,d.id FROM rider_devices d JOIN user_sessions session ON session.id=d.session_id LEFT JOIN rider_notification_preferences p ON p.rider_id=d.rider_id WHERE d.rider_id=$2::UUID AND d.enabled AND session.revoked_at IS NULL AND session.refresh_expires_at>NOW() AND CASE $3 WHEN 'ride_updates' THEN COALESCE(p.ride_updates,TRUE) WHEN 'account_alerts' THEN COALESCE(p.account_alerts,TRUE) ELSE COALESCE(p.promotions,FALSE) END ON CONFLICT DO NOTHING`, id, recipient, n.Type)
	return err
}
