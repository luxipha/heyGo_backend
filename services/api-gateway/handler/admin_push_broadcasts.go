package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"os"
	"strings"
	"time"
)

func fcmConfigured() bool {
	return strings.TrimSpace(os.Getenv("FCM_SERVICE_ACCOUNT_FILE")) != "" ||
		(strings.TrimSpace(os.Getenv("FCM_AUTH_MODE")) == "adc" && strings.TrimSpace(os.Getenv("FCM_PROJECT_ID")) != "")
}
func registerAdminPushBroadcastRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/communications/campaigns/:campaignID/broadcast", a.pushBroadcast)
	g.POST("/communications/campaigns/:campaignID/send", a.requireCSRF, a.sendPushBroadcast)
	g.POST("/communications/campaigns/:campaignID/cancel", a.requireCSRF, a.cancelPushBroadcast)
}

const broadcastJSON = `jsonb_build_object('id',b.id,'status',b.status,'purpose',b.purpose,'audience',b.audience,'title',b.title,'body',b.body,'scheduledFor',b.scheduled_for,'createdAt',b.created_at,
 'recipients',(SELECT COUNT(*) FROM admin_push_recipients r WHERE r.broadcast_id=b.id),
 'enqueuedRecipients',(SELECT COUNT(*) FROM admin_push_recipients r WHERE r.broadcast_id=b.id AND r.status='enqueued'),
 'skippedRecipients',(SELECT COUNT(*) FROM admin_push_recipients r WHERE r.broadcast_id=b.id AND r.status='skipped'),
 'sentDevices',(SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.source_event_id='broadcast:'||b.id::TEXT AND p.status='sent')+(SELECT COUNT(*) FROM rider_notification_push_deliveries p JOIN rider_notifications n ON n.id=p.notification_id WHERE n.source_event_id='broadcast:'||b.id::TEXT AND p.status='sent'),
 'failedDevices',(SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.source_event_id='broadcast:'||b.id::TEXT AND p.status='dead')+(SELECT COUNT(*) FROM rider_notification_push_deliveries p JOIN rider_notifications n ON n.id=p.notification_id WHERE n.source_event_id='broadcast:'||b.id::TEXT AND p.status='dead'))`

func (a *adminAPI) pushBroadcast(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var data json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `SELECT `+broadcastJSON+` FROM admin_push_broadcasts b WHERE b.campaign_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, contracts.APIResponse{Data: gin.H{"broadcast": nil, "providerConfigured": fcmConfigured()}})
		return
	}
	if err != nil {
		driverError(c, 503, "broadcast_unavailable", "Broadcast status unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"broadcast": data, "providerConfigured": fcmConfigured()}})
}
func (a *adminAPI) sendPushBroadcast(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var body struct {
		Version      int        `json:"version"`
		Purpose      string     `json:"purpose"`
		ScheduledFor *time.Time `json:"scheduledFor"`
		Reason       string     `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Version < 1 || (body.Purpose != "announcement" && body.Purpose != "marketing") || len(strings.TrimSpace(body.Reason)) < 5 || len(body.Reason) > 1000 {
		driverError(c, 400, "invalid_broadcast", "Choose a message purpose, version and change reason")
		return
	}
	now := time.Now().UTC()

	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "broadcast_unavailable", "Could not queue broadcast")
		return
	}
	defer tx.Rollback(c.Request.Context())
	var kind, category, audience, title, message string
	var version int
	err = tx.QueryRow(c.Request.Context(), `SELECT kind,category,audience,name,body,version FROM admin_campaign_drafts WHERE id=$1 FOR UPDATE`, id).Scan(&kind, &category, &audience, &title, &message, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "campaign_not_found", "Campaign not found")
		return
	}
	if err != nil {
		driverError(c, 503, "broadcast_unavailable", "Could not read campaign")
		return
	}
	if kind != "communication" || category != "Push" {
		driverError(c, 400, "unsupported_channel", "Only Push communications can be sent here")
		return
	}
	if audience != "Drivers" && audience != "Riders" && audience != "All users" {
		driverError(c, 400, "invalid_audience", "Save the draft with Drivers, Riders or All users as audience")
		return
	}
	if len(title)+len(message) > 2500 {
		driverError(c, 400, "push_message_too_long", "Shorten the title and message for push delivery")
		return
	}
	var existingVersion int
	var purpose string
	var scheduled *time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT source_version,purpose,scheduled_for FROM admin_push_broadcasts WHERE campaign_id=$1`, id).Scan(&existingVersion, &purpose, &scheduled)
	if err == nil {
		sameDate := (scheduled == nil && body.ScheduledFor == nil) || (scheduled != nil && body.ScheduledFor != nil && scheduled.Equal(*body.ScheduledFor))
		if existingVersion != body.Version || purpose != body.Purpose || !sameDate {
			driverError(c, 409, "broadcast_conflict", "A broadcast already exists for this draft")
			return
		}
		tx.Rollback(c.Request.Context())
		a.pushBroadcast(c)
		return
	}
	if err != pgx.ErrNoRows {
		driverError(c, 503, "broadcast_unavailable", "Could not queue broadcast")
		return
	}
	if version != body.Version {
		driverError(c, 409, "campaign_conflict", "Draft changed. Reload before sending")
		return
	}
	if body.ScheduledFor != nil && (!body.ScheduledFor.After(now) || body.ScheduledFor.After(now.Add(90*24*time.Hour))) {
		driverError(c, 400, "invalid_schedule", "Schedule must be in the future and within 90 days")
		return
	}
	broadcast := uuid.New()
	status := "queued"
	runAt := now
	if body.ScheduledFor != nil {
		status = "scheduled"
		runAt = *body.ScheduledFor
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_push_broadcasts(id,campaign_id,source_version,audience,purpose,title,body,scheduled_for,run_at,status,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::UUID)`, broadcast, id, version, audience, body.Purpose, title, message, body.ScheduledFor, runAt, status, currentAdmin(c).ID)
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_push_recipients(broadcast_id,user_id,audience) SELECT $1,id,CASE WHEN $2='Riders' THEN 'rider' WHEN roles @> ARRAY['driver'] THEN 'driver' ELSE 'rider' END FROM users WHERE active AND (($2='Drivers' AND roles @> ARRAY['driver']) OR ($2='Riders' AND roles @> ARRAY['rider']) OR ($2='All users' AND roles && ARRAY['driver','rider']))`, broadcast, audience)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE admin_campaign_drafts SET version=version+1,updated_at=NOW(),updated_by=$2::UUID WHERE id=$1`, id, currentAdmin(c).ID)
	}
	if err == nil {
		err = insertAdminAudit(c, tx, currentAdmin(c).ID, "campaign.push_queued", "", "campaign", id.String(), gin.H{"reason": body.Reason}, gin.H{"version": version}, gin.H{"broadcastId": broadcast, "status": status, "audience": audience, "purpose": body.Purpose, "runAt": runAt})
	}
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		driverError(c, 503, "broadcast_unavailable", "Could not queue broadcast")
		return
	}
	a.pushBroadcast(c)
}
func (a *adminAPI) cancelPushBroadcast(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil || len(strings.TrimSpace(body.Reason)) < 5 || len(body.Reason) > 1000 {
		driverError(c, 400, "invalid_reason", "Provide a cancellation reason")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "broadcast_unavailable", "Cancellation unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	result, err := tx.Exec(c.Request.Context(), `UPDATE admin_push_broadcasts SET status='cancelled' WHERE campaign_id=$1 AND status IN ('queued','scheduled')`, id)
	if err != nil {
		driverError(c, 503, "broadcast_unavailable", "Cancellation unavailable")
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "broadcast_started", "Broadcast has started or was already cancelled")
		return
	}
	if err = insertAdminAudit(c, tx, currentAdmin(c).ID, "campaign.push_cancelled", "", "campaign", id.String(), gin.H{"reason": body.Reason}, nil, gin.H{"status": "cancelled"}); err != nil || tx.Commit(c.Request.Context()) != nil {
		driverError(c, 503, "broadcast_unavailable", "Cancellation unavailable")
		return
	}
	a.pushBroadcast(c)
}
func StartPushBroadcastJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			if err := RunPushBroadcastJobsOnce(ctx, pool, 100); err != nil {
				logs.L().Warnw("Push broadcast queue failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func RunPushBroadcastJobsOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	if !fcmConfigured() {
		return nil
	}
	if _, err := newFCMSenderFromEnv(); err != nil {
		return err
	}
	return fanOutPushBroadcasts(ctx, pool, limit, time.Now().UTC())
}
func fanOutPushBroadcasts(ctx context.Context, pool *pgxpool.Pool, limit int, now time.Time) error {
	for i := 0; i < min(limit, 100); i++ {
		found, err := fanOutPushRecipient(ctx, pool, now)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
	return nil
}
func fanOutPushRecipient(ctx context.Context, pool *pgxpool.Pool, now time.Time) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var broadcast, title, body, purpose string
	err = tx.QueryRow(ctx, `SELECT id::TEXT,title,body,purpose FROM admin_push_broadcasts WHERE status IN ('queued','scheduled','processing') AND run_at<=$1 ORDER BY run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now).Scan(&broadcast, &title, &body, &purpose)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var owner, audience string
	err = tx.QueryRow(ctx, `SELECT user_id::TEXT,audience FROM admin_push_recipients WHERE broadcast_id=$1::UUID AND status='pending' ORDER BY user_id LIMIT 1 FOR UPDATE`, broadcast).Scan(&owner, &audience)
	if err == pgx.ErrNoRows {
		_, err = tx.Exec(ctx, `UPDATE admin_push_broadcasts SET status='enqueued' WHERE id=$1::UUID`, broadcast)
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT,0))`, owner); err != nil {
		return false, err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT active AND roles @> ARRAY[$2]::TEXT[] FROM users WHERE id=$1::UUID FOR SHARE`, owner, audience).Scan(&active)
	if err != nil {
		return false, err
	}
	status := "skipped"
	if active {
		payload, _ := json.Marshal(map[string]string{"broadcastId": broadcast})
		n := messaging.InboxNotification{Type: "announcement", Title: title, Message: body, Data: payload}
		if purpose == "marketing" {
			n.Type = "promotion"
		}
		if audience == "driver" {
			err = messaging.AppendNotificationTx(ctx, tx, owner, "broadcast:"+broadcast, n)
		} else {
			if purpose == "marketing" {
				n.Type = "promotions"
			} else {
				n.Type = "account_alerts"
			}
			err = messaging.AppendRiderBroadcastTx(ctx, tx, owner, broadcast, n)
		}
		if err != nil {
			return false, err
		}
		status = "enqueued"
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_push_recipients SET status=$3 WHERE broadcast_id=$1::UUID AND user_id=$2::UUID`, broadcast, owner, status); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_push_broadcasts SET status='processing' WHERE id=$1::UUID`, broadcast); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
