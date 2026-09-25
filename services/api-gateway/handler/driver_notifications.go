package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const driverNotificationPageSize = 30

type driverNotification struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
	ReadAt    *time.Time      `json:"readAt"`
}

func registerDriverNotificationRoutes(group *gin.RouterGroup, api *driverAPI) {
	group.GET("/notifications", api.driverNotifications)
	group.PATCH("/notifications/:id", api.markDriverNotificationRead)
	group.POST("/notifications/read-all", api.markAllDriverNotificationsRead)
}

func (a *driverAPI) driverNotifications(ctx *gin.Context) {
	var before any
	if raw := strings.TrimSpace(ctx.Query("cursor")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Notification cursor is invalid")
			return
		}
		before = id
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,type,title,message,data,created_at,read_at
		FROM driver_notifications WHERE driver_id=$1::UUID AND ($2::BIGINT IS NULL OR id<$2)
		ORDER BY id DESC LIMIT $3`, driverID(ctx), before, driverNotificationPageSize+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notifications are unavailable")
		return
	}
	defer rows.Close()
	items := make([]driverNotification, 0, driverNotificationPageSize+1)
	for rows.Next() {
		var item driverNotification
		if err := rows.Scan(&item.ID, &item.Type, &item.Title, &item.Message, &item.Data, &item.CreatedAt, &item.ReadAt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notifications are unavailable")
			return
		}
		item.CreatedAt = item.CreatedAt.UTC()
		if item.ReadAt != nil {
			readAt := item.ReadAt.UTC()
			item.ReadAt = &readAt
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notifications are unavailable")
		return
	}
	var unreadCount int64
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID AND read_at IS NULL`, driverID(ctx)).Scan(&unreadCount); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notifications are unavailable")
		return
	}
	var nextCursor *string
	if len(items) > driverNotificationPageSize {
		items = items[:driverNotificationPageSize]
		lastID := items[len(items)-1].ID
		nextCursor = &lastID
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items, "unreadCount": unreadCount}, Meta: contracts.APIMeta{NextCursor: nextCursor}})
}

func (a *driverAPI) markDriverNotificationRead(ctx *gin.Context) {
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil || id < 1 {
		driverError(ctx, http.StatusBadRequest, "invalid_notification_id", "Notification ID is invalid")
		return
	}
	var readAt time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `UPDATE driver_notifications SET read_at=COALESCE(read_at,NOW())
		WHERE id=$1 AND driver_id=$2::UUID RETURNING read_at`, id, driverID(ctx)).Scan(&readAt)
	if err == pgx.ErrNoRows {
		driverError(ctx, http.StatusNotFound, "notification_not_found", "Notification was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notification could not be marked read")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": strconv.FormatInt(id, 10), "readAt": readAt.UTC()}})
}

func (a *driverAPI) markAllDriverNotificationsRead(ctx *gin.Context) {
	result, err := a.pool.Exec(ctx.Request.Context(), `UPDATE driver_notifications SET read_at=NOW()
		WHERE driver_id=$1::UUID AND read_at IS NULL`, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "notifications_unavailable", "Notifications could not be marked read")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"updatedCount": result.RowsAffected()}})
}
