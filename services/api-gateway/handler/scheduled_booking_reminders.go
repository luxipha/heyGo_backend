package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
)

func (a *scheduledAPI) reminder(c *gin.Context) {
	var b struct {
		Enabled *bool `json:"enabled"`
		Version int64 `json:"version"`
	}
	id := c.Param("bookingID")
	u, _ := gatewayauth.CurrentUser(c)
	if uuid.Validate(id) != nil || c.ShouldBindJSON(&b) != nil || b.Enabled == nil || b.Version < 1 {
		driverError(c, 400, "invalid_reminder", "Supply enabled and booking version.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	tag, e := tx.Exec(c.Request.Context(), `UPDATE scheduled_rides SET rider_reminder_enabled=$3,version=version+1,updated_at=NOW() WHERE id=$1::uuid AND rider_id=$2::uuid AND version=$4 AND status IN('open','reserved','dispatched') AND pickup_at>NOW()`, id, u.ID, *b.Enabled, b.Version)
	if e != nil || tag.RowsAffected() != 1 {
		scheduleFail(c, e)
		return
	}
	// Preference changes return directly; only the due reminder creates a message.
	if e = tx.Commit(c.Request.Context()); e != nil {
		scheduleFail(c, e)
		return
	}
	heroData(c, gin.H{"bookingId": id, "reminderEnabled": *b.Enabled, "refreshScheduledRides": true})
}

func scheduledRiderRemindersOnce(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT id::text FROM scheduled_rides WHERE rider_reminder_enabled AND rider_reminder_sent_at IS NULL AND rider_reminder_at<=NOW() AND pickup_at>NOW() AND status IN('open','reserved','dispatched') ORDER BY rider_reminder_at,id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if _, e = tx.Exec(ctx, `UPDATE scheduled_rides SET rider_reminder_sent_at=NOW(),version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id); e != nil {
			return e
		}
		if e = scheduleEvent(ctx, tx, id, "scheduled.event.rider_reminder"); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
