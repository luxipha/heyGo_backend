package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"strings"
	"time"
)

func StartDriverDocumentReminderJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := RunDriverDocumentRemindersOnce(ctx, pool, 100); err != nil {
				logs.L().Warnw("Document reminders failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func RunDriverDocumentRemindersOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	if _, err := pool.Exec(ctx, `UPDATE driver_notification_push_deliveries p SET status='dead',lease_until=NULL,last_error='document_superseded' FROM driver_notifications n WHERE n.id=p.notification_id AND p.status IN ('pending','processing') AND n.type='document_expiry' AND NOT driver_document_reminder_current(n.data)`); err != nil {
		return err
	}
	return runDriverDocumentRemindersAt(ctx, pool, limit, time.Now().UTC())
}
func runDriverDocumentRemindersAt(ctx context.Context, pool *pgxpool.Pool, limit int, now time.Time) error {
	for i := 0; i < min(limit, 100); i++ {
		found, err := createDriverDocumentReminder(ctx, pool, now)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
	return nil
}
func createDriverDocumentReminder(ctx context.Context, pool *pgxpool.Pool, now time.Time) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id, owner, kind, stage string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT d.id::TEXT,d.driver_id::TEXT,d.type,d.expires_at,
 CASE WHEN d.expires_at<=$1 THEN 'expired' WHEN d.expires_at<=$1+INTERVAL '1 day' THEN '1_day' WHEN d.expires_at<=$1+INTERVAL '7 days' THEN '7_days' ELSE '30_days' END AS stage
 FROM driver_documents d JOIN users u ON u.id=d.driver_id
 WHERE u.active AND u.roles @> ARRAY['driver'] AND d.status='approved' AND d.expires_at<=$1+INTERVAL '30 days'
 AND NOT EXISTS(SELECT 1 FROM driver_documents newer WHERE newer.driver_id=d.driver_id AND newer.type=d.type AND (newer.submitted_at,newer.id)>(d.submitted_at,d.id))
 AND NOT EXISTS(SELECT 1 FROM driver_document_reminders r WHERE r.document_id=d.id AND r.expires_at=d.expires_at AND r.stage=CASE WHEN d.expires_at<=$1 THEN 'expired' WHEN d.expires_at<=$1+INTERVAL '1 day' THEN '1_day' WHEN d.expires_at<=$1+INTERVAL '7 days' THEN '7_days' ELSE '30_days' END)
 ORDER BY d.expires_at,d.id LIMIT 1 FOR UPDATE OF d SKIP LOCKED`, now).Scan(&id, &owner, &kind, &expires, &stage)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO driver_document_reminders(document_id,expires_at,stage) VALUES($1::UUID,$2,$3)`, id, expires, stage); err != nil {
		return false, err
	}
	label := strings.ReplaceAll(kind, "_", " ")
	message := fmt.Sprintf("Your %s expires on %s. Open Documents & Verification to review it.", label, expires.UTC().Format("2 Jan 2006"))
	title := "Document expiring"
	if stage == "expired" {
		title = "Document expired"
		message = fmt.Sprintf("Your %s has expired. Open Documents & Verification to submit a renewal.", label)
	}
	data, _ := json.Marshal(map[string]any{"documentId": id, "documentType": kind, "expiresAt": expires.UTC(), "stage": stage})
	source := fmt.Sprintf("document-expiry:%s:%d:%s", id, expires.UnixNano(), stage)
	if err = messaging.AppendNotificationTx(ctx, tx, owner, source, messaging.InboxNotification{Type: "document_expiry", Title: title, Message: message, Data: data}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
