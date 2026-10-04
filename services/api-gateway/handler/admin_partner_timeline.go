package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type partnerTimelineCursor struct {
	At  time.Time `json:"at"`
	Key string    `json:"key"`
}

func decodePartnerTimelineCursor(raw string) (partnerTimelineCursor, bool) {
	bytes, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(bytes) > 512 {
		return partnerTimelineCursor{}, false
	}
	var cursor partnerTimelineCursor
	if json.Unmarshal(bytes, &cursor) != nil || cursor.At.IsZero() || len(cursor.Key) == 0 || len(cursor.Key) > 120 {
		return partnerTimelineCursor{}, false
	}
	return cursor, true
}

func encodePartnerTimelineCursor(at time.Time, key string) string {
	bytes, _ := json.Marshal(partnerTimelineCursor{At: at, Key: key})
	return base64.RawURLEncoding.EncodeToString(bytes)
}

type partnerTimelineEvent struct {
	Key        string          `json:"key"`
	Action     string          `json:"action"`
	TargetID   string          `json:"targetId"`
	ActorID    *string         `json:"actorId"`
	ActorType  string          `json:"actorType"`
	OccurredAt time.Time       `json:"occurredAt"`
	Details    json.RawMessage `json:"details"`
}

// The timeline projects only stable application events and redacted audit
// fields. It is deliberately marked partial because older application changes
// may predate event capture or have only metadata-only audit rows.
func (a *adminAPI) partnerTimeline(ctx *gin.Context) {
	driverID, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	limit, err := parseDriverTripHistoryLimit(ctx.DefaultQuery("limit", "50"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursorAt any
	var cursorKey string
	if raw := ctx.Query("cursor"); raw != "" {
		cursor, valid := decodePartnerTimelineCursor(raw)
		if !valid {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Verification cursor is invalid")
			return
		}
		cursorAt, cursorKey = cursor.At, cursor.Key
	}
	var exists bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driverID).Scan(&exists); err != nil {
		reviewFailure(ctx)
		return
	}
	if !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `WITH events AS (
		SELECT 'application:'||p.driver_id::TEXT AS key,'application.created' AS action,p.driver_id::TEXT AS target_id,
			NULL::TEXT AS actor_id,'unknown' AS actor_type,p.created_at AS occurred_at,'{}'::JSONB AS details
		FROM driver_profiles p WHERE p.driver_id=$1::UUID
		UNION ALL
		SELECT 'document.submitted:'||d.id::TEXT,'document.submitted',d.id::TEXT,d.driver_id::TEXT,'driver',d.submitted_at,
			jsonb_build_object('type',d.type)
		FROM driver_documents d WHERE d.driver_id=$1::UUID
		UNION ALL
		SELECT 'document.reviewed:'||d.id::TEXT,'document.reviewed',d.id::TEXT,d.reviewed_by::TEXT,'staff',d.reviewed_at,
			jsonb_build_object('type',d.type,'status',d.status)
		FROM driver_documents d WHERE d.driver_id=$1::UUID AND d.reviewed_at IS NOT NULL
		UNION ALL
		SELECT 'inspection.received:'||i.id::TEXT,'inspection.report_received',i.id::TEXT,NULL::TEXT,'unknown',i.report_received_at,
			jsonb_build_object('status','report_received')
		FROM driver_inspections i WHERE i.driver_id=$1::UUID AND i.report_received_at IS NOT NULL
		UNION ALL
		SELECT 'inspection.reviewed:'||i.id::TEXT,'inspection.reviewed',i.id::TEXT,i.reviewed_by::TEXT,'staff',i.updated_at,
			jsonb_build_object('status',i.status)
		FROM driver_inspections i WHERE i.driver_id=$1::UUID AND i.reviewed_by IS NOT NULL
		UNION ALL
		SELECT 'audit:'||a.id::TEXT,a.action,COALESCE(a.target_id,a.target_driver_id::TEXT),a.admin_id::TEXT,'staff',a.created_at,
			jsonb_build_object('before',COALESCE(a.before_state,'{}'::JSONB),'after',COALESCE(a.after_state,'{}'::JSONB))
		FROM admin_audit a WHERE a.target_driver_id=$1::UUID
		AND a.action IN ('driver.profile.updated_by_admin','driver.vehicle.updated_by_admin','driver.manual_identity.review','driver.approval')
	)
	SELECT key,action,target_id,actor_id,actor_type,occurred_at,details FROM events
	WHERE $2::TIMESTAMPTZ IS NULL OR (occurred_at,key)<($2,$3)
	ORDER BY occurred_at DESC,key DESC LIMIT $4`, driverID, cursorAt, cursorKey, limit+1)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer rows.Close()
	items := make([]partnerTimelineEvent, 0, limit+1)
	for rows.Next() {
		var item partnerTimelineEvent
		if err := rows.Scan(&item.Key, &item.Action, &item.TargetID, &item.ActorID, &item.ActorType, &item.OccurredAt, &item.Details); err != nil {
			reviewFailure(ctx)
			return
		}
		if len(item.Key) > 6 && item.Key[:6] == "audit:" {
			var states map[string]json.RawMessage
			if json.Unmarshal(item.Details, &states) != nil {
				reviewFailure(ctx)
				return
			}
			before := safeAuditObject(states["before"])
			after := safeAuditObject(states["after"])
			item.Details, err = json.Marshal(gin.H{"before": before, "after": after})
			if err != nil {
				reviewFailure(ctx)
				return
			}
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		reviewFailure(ctx)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		value := encodePartnerTimelineCursor(last.OccurredAt, last.Key)
		next = &value
	}
	ctx.Header("X-History-Coverage", "partial-legacy")
	ctx.JSON(http.StatusOK, gin.H{"data": items, "meta": gin.H{"nextCursor": next, "coverage": "partial_legacy"}})
}
