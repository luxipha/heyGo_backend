package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

func supportDecisionReason(raw string) (string, bool) {
	reason := strings.TrimSpace(raw)
	return reason, len([]rune(reason)) >= 5 && len([]rune(reason)) <= 1000
}

func (a *supportAPI) adminCreateCase(ctx *gin.Context) {
	var body struct {
		OwnerID   string  `json:"ownerId"`
		Audience  string  `json:"audience"`
		TripID    *string `json:"tripId"`
		TopicCode string  `json:"topicCode"`
		Subject   string  `json:"subject"`
		Reason    string  `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_case", "Owner, topic, subject, and reason are required")
		return
	}
	owner, err := uuid.Parse(body.OwnerID)
	reason, validReason := supportDecisionReason(body.Reason)
	subject := strings.TrimSpace(body.Subject)
	if err != nil || !validReason || len([]rune(subject)) < 1 || len([]rune(subject)) > 160 || (body.Audience != "driver" && body.Audience != "rider") {
		driverError(ctx, http.StatusBadRequest, "invalid_case", "Valid owner, audience, subject, and reason are required")
		return
	}
	var tripID *uuid.UUID
	if body.TripID != nil && *body.TripID != "" {
		parsed, err := uuid.Parse(*body.TripID)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid Trip ID is required")
			return
		}
		tripID = &parsed
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var valid bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM users u JOIN support_topics t ON t.code=$3
		WHERE u.id=$1 AND u.active AND u.roles @> ARRAY[$2]::TEXT[] AND t.enabled
		AND (t.audience='both' OR t.audience=$2)
		AND (t.topic_kind='support' OR $4::UUID IS NOT NULL)
		AND ($4::UUID IS NULL OR EXISTS(SELECT 1 FROM trips tr WHERE tr.id=$4
			AND (($2='rider' AND tr.rider_id=u.id) OR ($2='driver' AND tr.assigned_driver_id=u.id)))))`, owner, body.Audience, body.TopicCode, tripID).Scan(&valid)
	if err != nil || !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_case_link", "Case topic, owner, or Trip link is invalid")
		return
	}
	caseID := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,subject,created_by_admin,staff_creation_reason)
		VALUES($1,$2,$3,$4,$5,$6,$7::UUID,$8)`, caseID, owner, body.Audience, body.TopicCode, tripID, subject, currentAdmin(ctx).ID, reason)
	if err == nil {
		err = adminAudit(ctx, tx, "support.case.created_by_staff", "", gin.H{"caseId": caseID.String(), "openedBy": owner.String(), "reason": reason})
	}
	if err == nil {
		payload, marshalErr := json.Marshal(gin.H{"caseId": caseID.String(), "status": "open"})
		err = marshalErr
		if err == nil {
			_, err = messaging.AppendEventTx(ctx.Request.Context(), tx, owner.String(), "support-case-staff-created:"+caseID.String(), "support.case.updated", payload)
		}
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": caseID.String(), "ownerId": owner.String(), "status": "open", "createdByAdminId": currentAdmin(ctx).ID}})
}

func (a *supportAPI) adminEscalateCase(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_escalation", "Reason is required")
		return
	}
	reason, valid := supportDecisionReason(body.Reason)
	if !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_escalation", "Reason must be 5 to 1000 characters")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Escalation could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var status string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT status FROM support_cases WHERE id=$1::UUID FOR UPDATE`, caseID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil || status == "resolved" || status == "closed" {
		driverError(ctx, http.StatusConflict, "support_case_closed", "Only open cases can be escalated")
		return
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_case_escalations(id,case_id,actor_admin_id,reason) VALUES($1,$2::UUID,$3::UUID,$4)`, id, caseID, currentAdmin(ctx).ID, reason)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE support_cases SET priority='high',status='in_progress',updated_at=NOW() WHERE id=$1::UUID`, caseID)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "support.case.escalated", "", gin.H{"caseId": caseID, "reason": reason, "status": "in_progress"})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Escalation could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "caseId": caseID, "priority": "high", "status": "in_progress"}})
}

func validSupportOutcome(topic, outcome string) bool {
	switch topic {
	case "fare", "payment":
		return outcome == "resolved_no_money" || outcome == "referred_to_finance" || outcome == "unable_to_resolve" || outcome == "withdrawn"
	case "lost_item":
		return outcome == "item_returned" || outcome == "unable_to_resolve" || outcome == "withdrawn"
	case "complaint":
		return outcome == "resolved_no_money" || outcome == "unable_to_resolve" || outcome == "withdrawn"
	}
	return false
}

func (a *supportAPI) adminCaseOutcome(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	var body struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_outcome", "Outcome and reason are required")
		return
	}
	reason, valid := supportDecisionReason(body.Reason)
	if !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_outcome", "Reason must be 5 to 1000 characters")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Outcome could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var owner, topic, status string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT opened_by::TEXT,topic_code,status FROM support_cases WHERE id=$1::UUID FOR UPDATE`, caseID).Scan(&owner, &topic, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil || status == "resolved" || status == "closed" {
		driverError(ctx, http.StatusConflict, "support_case_closed", "Only open cases can receive an outcome")
		return
	}
	if !validSupportOutcome(topic, body.Outcome) {
		driverError(ctx, http.StatusBadRequest, "invalid_outcome", "Outcome is not valid for this topic")
		return
	}
	var existing bool
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM support_case_outcomes WHERE case_id=$1::UUID)`, caseID).Scan(&existing); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Outcome could not be checked")
		return
	}
	if existing {
		driverError(ctx, http.StatusConflict, "outcome_already_recorded", "Case already has an outcome")
		return
	}
	newStatus := "resolved"
	if body.Outcome == "referred_to_finance" {
		newStatus = "in_progress"
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_case_outcomes(id,case_id,actor_admin_id,outcome,reason) VALUES($1,$2::UUID,$3::UUID,$4,$5)`, id, caseID, currentAdmin(ctx).ID, body.Outcome, reason)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE support_cases SET status=$2,updated_at=NOW() WHERE id=$1::UUID`, caseID, newStatus)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "support.case.outcome", "", gin.H{"caseId": caseID, "status": newStatus, "reason": reason, "kind": body.Outcome})
	}
	if err == nil {
		payload, marshalErr := json.Marshal(gin.H{"caseId": caseID, "status": newStatus})
		err = marshalErr
		if err == nil {
			_, err = messaging.AppendEventTx(ctx.Request.Context(), tx, owner, "support-case-outcome:"+id.String(), "support.case.updated", payload)
		}
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Outcome could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "caseId": caseID, "outcome": body.Outcome, "status": newStatus, "financeHandoff": body.Outcome == "referred_to_finance"}})
}

func (a *supportAPI) adminReadCaseOutcome(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	var id, actor, outcome, reason string
	var created time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,actor_admin_id::TEXT,outcome,reason,created_at FROM support_case_outcomes WHERE case_id=$1::UUID`, caseID).Scan(&id, &actor, &outcome, &reason, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: nil})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Outcome is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "caseId": caseID, "actorId": actor, "outcome": outcome, "reason": reason, "createdAt": created}})
}

func (a *supportAPI) adminCaseEscalations(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,actor_admin_id::TEXT,reason,created_at FROM support_case_escalations WHERE case_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 100`, caseID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Escalations are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, actor, reason string
		var created time.Time
		if rows.Scan(&id, &actor, &reason, &created) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Escalations are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "caseId": caseID, "actorId": actor, "reason": reason, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Escalations are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}
