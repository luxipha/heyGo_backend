package handler

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"time"
)

func riderPushEnabled() bool { return strings.EqualFold(os.Getenv("HEYGO_RIDER_PUSH_ENABLED"), "true") }
func claimRiderPushDelivery(ctx context.Context, pool *pgxpool.Pool) (*pendingPushDelivery, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Disabling preferences/devices also suppresses queued work before any send.
	_, err = tx.Exec(ctx, `UPDATE rider_notification_push_deliveries j SET status='dead',lease_until=NULL,last_error='delivery_disabled' FROM rider_devices d JOIN users u ON u.id=d.rider_id JOIN user_sessions session ON session.id=d.session_id LEFT JOIN rider_notification_preferences p ON p.rider_id=d.rider_id,rider_notifications n WHERE j.device_id=d.id AND j.notification_id=n.id AND (j.status='pending' OR (j.status='processing' AND j.lease_until<NOW())) AND (NOT d.enabled OR session.revoked_at IS NOT NULL OR session.refresh_expires_at<=NOW() OR NOT u.active OR NOT u.roles @> ARRAY['rider'] OR d.rider_id<>n.rider_id OR NOT CASE n.type WHEN 'ride_updates' THEN COALESCE(p.ride_updates,TRUE) WHEN 'account_alerts' THEN COALESCE(p.account_alerts,TRUE) ELSE COALESCE(p.promotions,FALSE) END)`)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE rider_notification_push_deliveries SET status='dead',lease_until=NULL,last_error='retry_limit_unresolved' WHERE attempts>=8 AND (status='pending' OR (status='processing' AND lease_until<NOW()))`); err != nil {
		return nil, err
	}
	var j pendingPushDelivery
	err = tx.QueryRow(ctx, `WITH candidate AS (SELECT id FROM rider_notification_push_deliveries WHERE (status='pending' AND next_attempt_at<=NOW()) OR (status='processing' AND lease_until<NOW()) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED), claimed AS (UPDATE rider_notification_push_deliveries j SET status='processing',attempts=j.attempts+1,lease_until=NOW()+INTERVAL '1 minute' FROM candidate c WHERE j.id=c.id RETURNING j.*) SELECT j.id,j.notification_id::TEXT,j.device_id::TEXT,d.push_token,n.title,n.message,n.type,n.data,j.attempts FROM claimed j JOIN rider_devices d ON d.id=j.device_id JOIN rider_notifications n ON n.id=j.notification_id`).Scan(&j.ID, &j.NotificationID, &j.DeviceID, &j.Token, &j.Title, &j.Body, &j.Type, &j.Data, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	j.Audience = "rider"
	return &j, nil
}
func riderPushStillAllowed(ctx context.Context, pool *pgxpool.Pool, j pendingPushDelivery) bool {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rider_notification_push_deliveries j JOIN rider_devices d ON d.id=j.device_id JOIN users u ON u.id=d.rider_id JOIN user_sessions session ON session.id=d.session_id JOIN rider_notifications n ON n.id=j.notification_id LEFT JOIN rider_notification_preferences p ON p.rider_id=d.rider_id WHERE j.id=$1 AND j.status='processing' AND j.attempts=$2 AND d.enabled AND session.revoked_at IS NULL AND session.refresh_expires_at>NOW() AND d.push_token=$3 AND u.active AND u.roles @> ARRAY['rider'] AND d.rider_id=n.rider_id AND CASE n.type WHEN 'ride_updates' THEN COALESCE(p.ride_updates,TRUE) WHEN 'account_alerts' THEN COALESCE(p.account_alerts,TRUE) ELSE COALESCE(p.promotions,FALSE) END)`, j.ID, j.Attempts, j.Token).Scan(&ok)
	return err == nil && ok
}
func finishRiderPushDelivery(ctx context.Context, pool *pgxpool.Pool, j pendingPushDelivery, sendErr error) {
	if sendErr == nil {
		_, _ = pool.Exec(ctx, `UPDATE rider_notification_push_deliveries SET status='sent',sent_at=NOW(),lease_until=NULL,last_error='' WHERE id=$1 AND attempts=$2 AND status='processing'`, j.ID, j.Attempts)
		return
	}
	if errors.Is(sendErr, errFCMUnregistered) {
		// An old token's provider response cannot disable a newly rotated token.
		_, _ = pool.Exec(ctx, `UPDATE rider_devices SET enabled=FALSE,push_token='',updated_at=NOW() WHERE id=$1::UUID AND push_token=$2`, j.DeviceID, j.Token)
	}
	dead := errors.Is(sendErr, errFCMUnregistered) || j.Attempts >= 8
	reason := "provider_delivery_failed"
	if errors.Is(sendErr, errFCMUnregistered) {
		reason = "unregistered_device"
	}
	delay := time.Second * time.Duration(1<<min(j.Attempts, 10))
	_, _ = pool.Exec(ctx, `UPDATE rider_notification_push_deliveries SET status=CASE WHEN $3 THEN 'dead' ELSE 'pending' END,lease_until=NULL,next_attempt_at=NOW()+$4::INTERVAL,last_error=$5 WHERE id=$1 AND attempts=$2 AND status='processing'`, j.ID, j.Attempts, dead, delay.String(), reason)
}
func claimAnyPushDelivery(ctx context.Context, pool *pgxpool.Pool, riderFirst bool) (*pendingPushDelivery, error) {
	if riderFirst && riderPushEnabled() {
		j, e := claimRiderPushDelivery(ctx, pool)
		if e != nil || j != nil {
			return j, e
		}
	}
	j, e := claimPushDelivery(ctx, pool)
	if e != nil || j != nil {
		return j, e
	}
	if !riderFirst && riderPushEnabled() {
		return claimRiderPushDelivery(ctx, pool)
	}
	return nil, nil
}
func sendClaimedPush(ctx context.Context, pool *pgxpool.Pool, s *fcmSender, j pendingPushDelivery) error {
	if j.Audience != "rider" {
		if !driverPushStillAllowed(ctx, pool, j) {
			return nil
		}
		return s.send(ctx, j.Token, j.Title, j.Body, j.NotificationID, j.Type)
	}
	if !riderPushStillAllowed(ctx, pool, j) {
		_, err := pool.Exec(ctx, `UPDATE rider_notification_push_deliveries SET status='pending',lease_until=NULL,next_attempt_at=NOW()+INTERVAL '2 seconds',last_error='registration_or_preference_changed' WHERE id=$1 AND attempts=$2 AND status='processing'`, j.ID, j.Attempts)
		return err
	}
	return s.sendWithData(ctx, j.Token, j.Title, j.Body, map[string]string{"notification_id": j.NotificationID, "audience": "rider", "schema_version": "1"})
}
