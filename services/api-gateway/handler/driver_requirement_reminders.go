package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

// StartDriverRequirementReminderJobs tells drivers who registered before an item
// became Required, while they are still inside the grace window: once straight
// after the change, again 7 days before the deadline, and 1 day before it.
func StartDriverRequirementReminderJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := RunDriverRequirementRemindersOnce(ctx, pool, 500); err != nil {
				logs.L().Warnw("Requirement reminders failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func RunDriverRequirementRemindersOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	for i := 0; i < limit; i++ {
		found, err := createDriverRequirementReminder(ctx, pool)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
	return nil
}

var requirementLabels = map[string]string{
	"vehicle_license":    "vehicle licence",
	"roadworthiness":     "roadworthiness certificate",
	"auto_insurance":     "auto insurance",
	"hackney_permit":     "hackney permit",
	"vehicle_inspection": "vehicle inspection report",
}

// requirementReminderSQL picks one driver who still lacks a newly required item
// and has not yet been sent the reminder for the current stage.
const requirementReminderSQL = `
SELECT u.id::TEXT, c.type, c.required_since, c.required_since + make_interval(days => c.grace_days) AS deadline, s.stage
FROM driver_requirement_config c
JOIN users u ON u.created_at < c.required_since AND u.active AND u.roles @> ARRAY['driver']
CROSS JOIN LATERAL (SELECT CASE
  WHEN NOW() >= c.required_since + make_interval(days => c.grace_days) - INTERVAL '1 day' THEN '1_day'
  WHEN NOW() >= c.required_since + make_interval(days => c.grace_days) - INTERVAL '7 days' THEN '7_days'
  ELSE 'announce' END AS stage) s
WHERE c.mode='required' AND c.required_since IS NOT NULL
  AND NOW() < c.required_since + make_interval(days => c.grace_days)
  AND NOT CASE WHEN c.type='vehicle_inspection'
    THEN EXISTS(SELECT 1 FROM driver_inspections_records i JOIN driver_vehicle_records v ON v.id=i.vehicle_id WHERE v.driver_id=u.id AND i.status='completed')
    ELSE COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) FROM driver_documents_records d WHERE d.driver_id=u.id AND d.type=c.type ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE) END
  AND NOT EXISTS(SELECT 1 FROM driver_requirement_reminders r WHERE r.driver_id=u.id AND r.type=c.type AND r.required_since=c.required_since AND r.stage=s.stage)
ORDER BY c.required_since,u.id LIMIT 1`

func createDriverRequirementReminder(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var owner, kind, stage string
	var since, deadline time.Time
	err = tx.QueryRow(ctx, requirementReminderSQL).Scan(&owner, &kind, &since, &deadline, &stage)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO driver_requirement_reminders(driver_id,type,required_since,stage) VALUES($1::UUID,$2,$3,$4) ON CONFLICT DO NOTHING`, owner, kind, since, stage); err != nil {
		return false, err
	}
	label := requirementLabels[kind]
	by := deadline.UTC().Format("2 Jan 2006")
	title, message := "New requirement", fmt.Sprintf("heyGo now requires your %s. Add it in Documents & Verification by %s to keep going online.", label, by)
	switch stage {
	case "7_days":
		title, message = "Requirement due in 7 days", fmt.Sprintf("Add your %s in Documents & Verification by %s to keep going online.", label, by)
	case "1_day":
		title, message = "Requirement due tomorrow", fmt.Sprintf("Add your %s in Documents & Verification by %s, or you won't be able to go online.", label, by)
	}
	data, _ := json.Marshal(map[string]any{"requirementType": kind, "deadline": deadline.UTC(), "stage": stage})
	source := fmt.Sprintf("requirement-due:%s:%s:%d:%s", owner, kind, since.UnixNano(), stage)
	if err = messaging.AppendNotificationTx(ctx, tx, owner, source, messaging.InboxNotification{Type: "requirement_due", Title: title, Message: message, Data: data}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
