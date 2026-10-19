package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffIncident struct {
	ID              string     `json:"id"`
	CaseID          string     `json:"caseId"`
	TripID          *string    `json:"tripId"`
	ReporterID      string     `json:"reporterId"`
	Subject         string     `json:"subject"`
	Status          string     `json:"status"`
	Severity        string     `json:"severity"`
	AssignedAdminID *string    `json:"assignedAdminId"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
	SOS             bool       `json:"sos"`
}

const staffIncidentSelect = `SELECT i.id::TEXT,i.case_id::TEXT,c.trip_id::TEXT,c.opened_by::TEXT,c.subject,i.status,i.severity,i.assigned_admin_id::TEXT,i.created_at,i.updated_at,i.resolved_at,EXISTS(SELECT 1 FROM sos_intakes s WHERE s.incident_id=i.id) FROM safety_incidents i JOIN support_cases c ON c.id=i.case_id `

func scanStaffIncident(row pgx.Row) (staffIncident, error) {
	var item staffIncident
	err := row.Scan(&item.ID, &item.CaseID, &item.TripID, &item.ReporterID, &item.Subject, &item.Status, &item.Severity, &item.AssignedAdminID, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt, &item.SOS)
	return item, err
}

func registerAdminSafetyRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/safety/summary", a.safetySummary)
	group.GET("/safety/incidents", a.listSafetyIncidents)
	group.GET("/safety/sos-alerts", a.listSOSAlerts)
	group.GET("/safety/incidents/:incidentID", a.safetyIncident)
	group.GET("/safety/incidents/:incidentID/actions", a.safetyIncidentActions)
	group.GET("/safety/incidents/:incidentID/sos", a.safetySOSEvidence)
	group.PATCH("/safety/incidents/:incidentID", a.requireCSRF, a.updateSafetyIncident)
}

func (a *adminAPI) listSOSAlerts(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), staffIncidentSelect+` WHERE i.status='open'
		AND EXISTS(SELECT 1 FROM sos_intakes s WHERE s.incident_id=i.id)
		AND ($1::TIMESTAMPTZ IS NULL OR (i.created_at,i.id)<($1,$2::UUID))
		ORDER BY i.created_at DESC,i.id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "SOS alerts are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffIncident, 0, limit+1)
	for rows.Next() {
		item, err := scanStaffIncident(rows)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "SOS alerts are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "SOS alerts are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[limit-1].CreatedAt, ID: items[limit-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) safetyIncidentActions(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("incidentID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_incident_id", "Valid incident ID is required")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT actor_admin_id::TEXT,old_status,new_status,old_severity,new_severity,reason,created_at FROM safety_incident_actions WHERE incident_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident actions are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var actor, oldStatus, newStatus, oldSeverity, newSeverity, reason string
		var created time.Time
		if rows.Scan(&actor, &oldStatus, &newStatus, &oldSeverity, &newSeverity, &reason, &created) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident actions are unavailable")
			return
		}
		items = append(items, gin.H{"actorId": actor, "oldStatus": oldStatus, "newStatus": newStatus, "oldSeverity": oldSeverity, "newSeverity": newSeverity, "reason": reason, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident actions are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}

func validIncidentStatus(value string) bool {
	switch value {
	case "open", "acknowledged", "investigating", "escalated", "resolved":
		return true
	}
	return false
}
func validIncidentSeverity(value string) bool {
	switch value {
	case "untriaged", "low", "medium", "high", "critical":
		return true
	}
	return false
}

func (a *adminAPI) listSafetyIncidents(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	status := ctx.DefaultQuery("status", "all")
	if status != "all" && status != "attention" && !validIncidentStatus(status) {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Incident status is invalid")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), staffIncidentSelect+` WHERE ($1='all' OR ($1='attention' AND i.status<>'resolved') OR i.status=$1) AND ($2::TIMESTAMPTZ IS NULL OR (i.created_at,i.id)<($2,$3::UUID)) ORDER BY i.created_at DESC,i.id DESC LIMIT $4`, status, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incidents are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffIncident, 0, limit+1)
	for rows.Next() {
		item, err := scanStaffIncident(rows)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incidents are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incidents are unavailable")
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

func (a *adminAPI) safetyIncident(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("incidentID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_incident_id", "Valid incident ID is required")
		return
	}
	item, err := scanStaffIncident(a.pool.QueryRow(ctx.Request.Context(), staffIncidentSelect+` WHERE i.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "incident_not_found", "Incident was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}

func (a *adminAPI) updateSafetyIncident(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("incidentID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_incident_id", "Valid incident ID is required")
		return
	}
	var body struct {
		Status          *string `json:"status"`
		Severity        *string `json:"severity"`
		AssignedAdminID *string `json:"assignedAdminId"`
		Reason          string  `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status == nil && body.Severity == nil && body.AssignedAdminID == nil) || len(strings.TrimSpace(body.Reason)) < 1 || len(body.Reason) > 1000 || (body.Status != nil && !validIncidentStatus(*body.Status)) || (body.Severity != nil && !validIncidentSeverity(*body.Severity)) {
		driverError(ctx, http.StatusBadRequest, "invalid_incident_update", "Valid change and reason are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident could not be updated")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	old, err := scanStaffIncident(tx.QueryRow(ctx.Request.Context(), staffIncidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "incident_not_found", "Incident was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident could not be updated")
		return
	}
	status, severity, assignee := old.Status, old.Severity, old.AssignedAdminID
	if body.Status != nil {
		status = *body.Status
	}
	if status != old.Status && old.Status == "open" && status != "acknowledged" {
		var sos bool
		if err := tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM sos_intakes WHERE incident_id=$1)`, id).Scan(&sos); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident could not be checked")
			return
		} else if sos {
			driverError(ctx, http.StatusConflict, "sos_acknowledgment_required", "Acknowledge the SOS before changing its response state")
			return
		}
	}
	if body.Severity != nil {
		severity = *body.Severity
	}
	if body.AssignedAdminID != nil {
		if *body.AssignedAdminID == "" {
			assignee = nil
		} else {
			staffID, parseErr := uuid.Parse(*body.AssignedAdminID)
			if parseErr != nil {
				driverError(ctx, http.StatusBadRequest, "invalid_assignee", "Assignee must be active Safety staff")
				return
			}
			var permitted bool
			if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM admin_users WHERE id=$1 AND active AND role IN ('safety','super_admin'))`, staffID).Scan(&permitted); err != nil || !permitted {
				driverError(ctx, http.StatusBadRequest, "invalid_assignee", "Assignee must be active Safety staff")
				return
			}
			value := staffID.String()
			assignee = &value
		}
	}
	var resolvedAt *time.Time
	if status == "resolved" {
		resolvedAt = old.ResolvedAt
		if resolvedAt == nil {
			now := time.Now().UTC()
			resolvedAt = &now
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE safety_incidents SET status=$2,severity=$3,assigned_admin_id=$4::UUID,resolved_at=$5,updated_at=NOW() WHERE id=$1`, id, status, severity, assignee, resolvedAt)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO safety_incident_actions(incident_id,actor_admin_id,old_status,new_status,old_severity,new_severity,reason) VALUES($1,$2::UUID,$3,$4,$5,$6,$7)`, id, currentAdmin(ctx).ID, old.Status, status, old.Severity, severity, strings.TrimSpace(body.Reason))
	}
	if err == nil {
		err = adminAudit(ctx, tx, "safety.incident.updated", "", gin.H{"incidentId": id.String(), "caseId": old.CaseID, "oldStatus": old.Status, "newStatus": status, "oldSeverity": old.Severity, "newSeverity": severity, "reason": strings.TrimSpace(body.Reason)})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident could not be updated")
		return
	}
	updated, err := scanStaffIncident(a.pool.QueryRow(ctx.Request.Context(), staffIncidentSelect+` WHERE i.id=$1`, id))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_unavailable", "Incident was updated but could not be read")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: updated})
}
