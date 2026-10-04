package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Only these non-secret fields may appear in the staff-facing audit diff.
var auditSafeFields = map[string]bool{
	"status": true, "oldStatus": true, "newStatus": true,
	"severity": true, "oldSeverity": true, "newSeverity": true,
	"roles": true, "oldRoles": true, "newRoles": true,
	"active": true, "oldActive": true, "newActive": true,
	"emailVerified": true, "emailVerificationPending": true,
	"invitationPending": true, "version": true,
	"enabled": true, "sortOrder": true, "assignedAdminId": true,
	"restricted": true, "kind": true,
	"amountKobo": true, "maxRequestKobo": true, "maxDailyKobo": true,
}

func safeAuditObject(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return map[string]any{}
	}
	output := make(map[string]any)
	for key, item := range input {
		if auditSafeFields[key] {
			output[key] = item
		}
	}
	return output
}

func adminAuditTarget(action, driverID string, details any) (string, string) {
	var input map[string]any
	raw, _ := json.Marshal(details)
	_ = json.Unmarshal(raw, &input)
	for _, candidate := range []struct{ key, kind string }{
		{"targetStaffId", "staff"}, {"incidentId", "incident"}, {"refundId", "refund"}, {"caseId", "support_case"},
		{"geofenceId", "geofence"}, {"policyId", "policy"}, {"ruleId", "statutory_rule"},
		{"claimId", "no_show_claim"}, {"inspectionId", "inspection"}, {"documentId", "document"},
		{"tripId", "trip"}, {"requestId", "request"},
	} {
		if value, ok := input[candidate.key].(string); ok && value != "" {
			return candidate.kind, value
		}
	}
	if driverID != "" {
		return "driver", driverID
	}
	if strings.HasPrefix(action, "support.topic.") {
		if code, ok := input["code"].(string); ok {
			return "support_topic", code
		}
	}
	if strings.HasPrefix(action, "privacy.resend_settings") {
		return "settings", "resend"
	}
	return "", ""
}

func auditStates(details any) (map[string]any, map[string]any) {
	input := safeAuditObject(details)
	raw, _ := json.Marshal(details)
	var full map[string]any
	_ = json.Unmarshal(raw, &full)
	before := make(map[string]any)
	after := make(map[string]any)
	if nested, ok := full["before"]; ok {
		for key, value := range safeAuditObject(nested) {
			before[key] = value
		}
	}
	if nested, ok := full["after"]; ok {
		for key, value := range safeAuditObject(nested) {
			after[key] = value
		}
	}
	for key, value := range input {
		switch {
		case strings.HasPrefix(key, "old") && len(key) > 3:
			before[strings.ToLower(key[3:4])+key[4:]] = value
		case strings.HasPrefix(key, "new") && len(key) > 3:
			after[strings.ToLower(key[3:4])+key[4:]] = value
		default:
			after[key] = value
		}
	}
	for _, field := range []string{"status", "severity", "active", "roles"} {
		if value, ok := full[field+"Before"]; ok {
			before[field] = value
		}
		if value, ok := full[field+"After"]; ok {
			after[field] = value
		}
	}
	return before, after
}

func insertAdminAudit(ctx *gin.Context, tx pgx.Tx, actorID, action, targetDriverID, targetType, targetID string, details, before, after any) error {
	detailJSON, err := json.Marshal(details)
	if err != nil {
		return err
	}
	beforeJSON, err := json.Marshal(safeAuditObject(before))
	if err != nil {
		return err
	}
	afterJSON, err := json.Marshal(safeAuditObject(after))
	if err != nil {
		return err
	}
	var reason *string
	var input map[string]any
	_ = json.Unmarshal(detailJSON, &input)
	if value, ok := input["reason"].(string); ok && value != "" {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			reason = &trimmed
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_audit(id,admin_id,action,target_driver_id,target_type,target_id,details,before_state,after_state,reason)
		VALUES($1,$2::UUID,$3,NULLIF($4,'')::UUID,NULLIF($5,''),NULLIF($6,''),$7::JSONB,$8::JSONB,$9::JSONB,$10)`,
		uuid.New(), actorID, action, targetDriverID, targetType, targetID, detailJSON, beforeJSON, afterJSON, reason)
	return err
}

func staffAuditChange(ctx *gin.Context, tx pgx.Tx, action, staffID string, before, after any) error {
	return staffAuditChangeForActor(ctx, tx, currentAdmin(ctx).ID, action, staffID, before, after)
}

func staffAuditChangeWithReason(ctx *gin.Context, tx pgx.Tx, action, staffID, reason string, before, after any) error {
	return insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, action, "", "staff", staffID, gin.H{"reason": reason}, before, after)
}

func staffAuditChangeForActor(ctx *gin.Context, tx pgx.Tx, actorID, action, staffID string, before, after any) error {
	return insertAdminAudit(ctx, tx, actorID, action, "", "staff", staffID, gin.H{}, before, after)
}
