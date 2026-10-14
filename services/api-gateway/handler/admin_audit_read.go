package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffAuditEvent struct {
	ID             string          `json:"id"`
	ActorID        string          `json:"actorId"`
	Action         string          `json:"action"`
	TargetDriverID *string         `json:"targetDriverId,omitempty"`
	TargetType     *string         `json:"targetType,omitempty"`
	TargetID       *string         `json:"targetId,omitempty"`
	Before         json.RawMessage `json:"before"`
	After          json.RawMessage `json:"after"`
	Reason         *string         `json:"reason,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

func registerAdminAuditRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/audit", a.listStaffAudit)
	group.GET("/audit/:eventID", a.staffAuditDetail)
}

func (a *adminAPI) listStaffAudit(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	action := ctx.Query("action")
	actorID, targetType, targetID := ctx.Query("actorId"), ctx.Query("targetType"), ctx.Query("targetId")
	if len(action) > 100 || len(targetType) > 60 || len(targetID) > 100 || (actorID != "" && !validAuditUUID(actorID)) {
		driverError(ctx, http.StatusBadRequest, "invalid_audit_filter", "Audit filter is invalid")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,admin_id::TEXT,action,target_driver_id::TEXT,target_type,target_id,COALESCE(before_state,'{}'::JSONB),COALESCE(after_state,'{}'::JSONB),reason,created_at FROM admin_audit
		WHERE ($1='' OR action=$1) AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2,$3::UUID))
		AND ($5='' OR admin_id=$5::UUID) AND ($6='' OR target_type=$6) AND ($7='' OR target_id=$7)
		ORDER BY created_at DESC,id DESC LIMIT $4`, action, at, id, limit+1, actorID, targetType, targetID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "audit_unavailable", "Audit events are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffAuditEvent, 0, limit+1)
	for rows.Next() {
		var item staffAuditEvent
		if rows.Scan(&item.ID, &item.ActorID, &item.Action, &item.TargetDriverID, &item.TargetType, &item.TargetID, &item.Before, &item.After, &item.Reason, &item.CreatedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "audit_unavailable", "Audit events are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "audit_unavailable", "Audit events are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffAuditDetail(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("eventID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_audit_id", "Valid audit ID is required")
		return
	}
	var item staffAuditEvent
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,admin_id::TEXT,action,target_driver_id::TEXT,target_type,target_id,COALESCE(before_state,'{}'::JSONB),COALESCE(after_state,'{}'::JSONB),reason,created_at FROM admin_audit WHERE id=$1`, id).Scan(&item.ID, &item.ActorID, &item.Action, &item.TargetDriverID, &item.TargetType, &item.TargetID, &item.Before, &item.After, &item.Reason, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "audit_not_found", "Audit event was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "audit_unavailable", "Audit event is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}

func validAuditUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
