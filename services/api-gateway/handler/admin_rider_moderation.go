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

func registerAdminRiderModerationRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/riders/:riderID/restrictions", a.listRiderRestrictions)
	group.POST("/riders/:riderID/restrictions", a.requireCSRF, a.restrictRider)
	group.POST("/riders/:riderID/restrictions/:restrictionID/lift", a.requireCSRF, a.liftRiderRestriction)
	group.GET("/riders/:riderID/flags", a.listRiderFlags)
	group.POST("/riders/:riderID/flags", a.requireCSRF, a.createRiderFlag)
	group.POST("/riders/:riderID/flags/:flagID/resolve", a.requireCSRF, a.resolveRiderFlag)
}

func moderationRiderID(ctx *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param("riderID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_rider_id", "Valid Rider ID is required")
		return uuid.Nil, false
	}
	return id, true
}

func moderationReason(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, len(value) >= 5 && len(value) <= 1000
}

func caseBelongsToRider(ctx *gin.Context, tx pgx.Tx, riderID, caseID uuid.UUID, kind string, incidentID *uuid.UUID) (bool, error) {
	var valid bool
	err := tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM support_cases c
		LEFT JOIN trips t ON t.id=c.trip_id
		WHERE c.id=$1 AND (c.opened_by=$2 OR t.rider_id=$2)
		AND (($3='safety' AND c.topic_code IN ('safety','general-safety')
		 AND c.audience='driver' AND t.rider_id=$2
		 AND EXISTS(SELECT 1 FROM users reporter WHERE reporter.id=c.opened_by AND reporter.roles @> ARRAY['driver']::TEXT[])
		 AND ($4::UUID IS NULL OR EXISTS(SELECT 1 FROM safety_incidents i WHERE i.id=$4 AND i.case_id=c.id)))
		 OR ($3='payment' AND c.topic_code IN ('fare','payment'))
		 OR ($3='support' AND c.topic_code NOT IN ('safety','general-safety','fare','payment'))))`, caseID, riderID, kind, incidentID).Scan(&valid)
	return valid, err
}

func lockActiveRider(ctx *gin.Context, tx pgx.Tx, riderID uuid.UUID) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx.Request.Context(), `SELECT active FROM users WHERE id=$1 AND roles @> ARRAY['rider']::TEXT[] FOR UPDATE`, riderID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return active, err
}

func (a *adminAPI) restrictRider(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	var body struct {
		CaseID   string    `json:"caseId"`
		Reason   string    `json:"reason"`
		ReviewAt time.Time `json:"reviewAt"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_restriction", "Case, reason, and review date are required")
		return
	}
	caseID, caseErr := uuid.Parse(body.CaseID)
	reason, validReason := moderationReason(body.Reason)
	if caseErr != nil || !validReason || !body.ReviewAt.After(time.Now().UTC()) {
		driverError(ctx, http.StatusBadRequest, "invalid_restriction", "Valid Safety case, reason, and future review date are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	active, err := lockActiveRider(ctx, tx, riderID)
	if err != nil || !active {
		driverError(ctx, http.StatusNotFound, "rider_not_found", "Active Rider was not found")
		return
	}
	linked, err := caseBelongsToRider(ctx, tx, riderID, caseID, "safety", nil)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Case could not be checked")
		return
	}
	if !linked {
		driverError(ctx, http.StatusBadRequest, "invalid_case", "A Driver-opened Safety case linked to this Rider's Trip is required")
		return
	}
	var restricted bool
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT rider_trip_restricted FROM users WHERE id=$1`, riderID).Scan(&restricted); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be checked")
		return
	}
	if restricted {
		driverError(ctx, http.StatusConflict, "rider_already_restricted", "Rider already has an active restriction")
		return
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_restrictions(id,rider_id,case_id,status,reason,review_at,applied_by) VALUES($1,$2,$3,'active',$4,$5,$6::UUID)`, id, riderID, caseID, reason, body.ReviewAt, currentAdmin(ctx).ID)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE users SET rider_trip_restricted=TRUE,updated_at=NOW() WHERE id=$1`, riderID)
	}
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_restriction_actions(id,restriction_id,actor_admin_id,action,reason) VALUES($1,$2,$3::UUID,'applied',$4)`, uuid.New(), id, currentAdmin(ctx).ID, reason)
	}
	if err == nil {
		err = insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, "rider.restriction.applied", "", "rider", riderID.String(), gin.H{"restrictionId": id.String(), "caseId": caseID.String(), "reason": reason}, gin.H{"restricted": false}, gin.H{"restricted": true})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "riderId": riderID.String(), "caseId": caseID.String(), "status": "active", "reviewAt": body.ReviewAt, "reason": reason}})
}

func (a *adminAPI) liftRiderRestriction(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	id, err := uuid.Parse(ctx.Param("restrictionID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_restriction_id", "Valid restriction ID is required")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_reason", "Reason is required")
		return
	}
	reason, valid := moderationReason(body.Reason)
	if !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_reason", "Reason must be 5 to 1000 characters")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be lifted")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	active, err := lockActiveRider(ctx, tx, riderID)
	if err != nil || !active {
		driverError(ctx, http.StatusNotFound, "rider_not_found", "Active Rider was not found")
		return
	}
	var status string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT status FROM rider_restrictions WHERE id=$1 AND rider_id=$2 FOR UPDATE`, id, riderID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "restriction_not_found", "Restriction was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be read")
		return
	}
	if status != "active" {
		driverError(ctx, http.StatusConflict, "restriction_already_lifted", "Restriction has already been lifted")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE rider_restrictions SET status='lifted',lifted_by=$2::UUID,lifted_at=NOW(),lift_reason=$3 WHERE id=$1`, id, currentAdmin(ctx).ID, reason)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE users SET rider_trip_restricted=FALSE,updated_at=NOW() WHERE id=$1`, riderID)
	}
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_restriction_actions(id,restriction_id,actor_admin_id,action,reason) VALUES($1,$2,$3::UUID,'lifted',$4)`, uuid.New(), id, currentAdmin(ctx).ID, reason)
	}
	if err == nil {
		err = insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, "rider.restriction.lifted", "", "rider", riderID.String(), gin.H{"restrictionId": id.String(), "reason": reason}, gin.H{"restricted": true}, gin.H{"restricted": false})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restriction could not be lifted")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "riderId": riderID.String(), "status": "lifted"}})
}

func (a *adminAPI) listRiderRestrictions(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	if !adminHasPermission(ctx, permissionRiderRestrict) {
		driverError(ctx, http.StatusForbidden, "admin_permission_denied", "Safety permission is required")
		return
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,case_id::TEXT,status,reason,review_at,applied_by::TEXT,applied_at,lifted_by::TEXT,lifted_at,lift_reason FROM rider_restrictions WHERE rider_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (applied_at,id)<($2,$3::UUID)) ORDER BY applied_at DESC,id DESC LIMIT $4`, riderID, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restrictions are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit+1)
	ids := make([]string, 0, limit+1)
	times := make([]time.Time, 0, limit+1)
	for rows.Next() {
		var rid, caseID, status, reason, appliedBy string
		var reviewAt, appliedAt time.Time
		var liftedBy, liftReason *string
		var liftedAt *time.Time
		if rows.Scan(&rid, &caseID, &status, &reason, &reviewAt, &appliedBy, &appliedAt, &liftedBy, &liftedAt, &liftReason) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restrictions are unavailable")
			return
		}
		items = append(items, gin.H{"id": rid, "riderId": riderID.String(), "caseId": caseID, "status": status, "reason": reason, "reviewAt": reviewAt, "appliedBy": appliedBy, "appliedAt": appliedAt, "liftedBy": liftedBy, "liftedAt": liftedAt, "liftReason": liftReason})
		ids = append(ids, rid)
		times = append(times, appliedAt)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Restrictions are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: times[limit-1], ID: ids[limit-1]})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func canManageRiderFlag(ctx *gin.Context, kind string) bool {
	switch kind {
	case "safety":
		return adminHasPermission(ctx, permissionSafetyWrite)
	case "support", "payment":
		return adminHasPermission(ctx, permissionSupportWrite)
	default:
		return false
	}
}

func (a *adminAPI) createRiderFlag(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	var body struct {
		Kind       string    `json:"kind"`
		CaseID     string    `json:"caseId"`
		IncidentID *string   `json:"incidentId"`
		Reason     string    `json:"reason"`
		ReviewAt   time.Time `json:"reviewAt"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_flag", "Kind, linked case, reason, and review date are required")
		return
	}
	if !canManageRiderFlag(ctx, body.Kind) {
		driverError(ctx, http.StatusForbidden, "admin_permission_denied", "Flag type requires its owning staff role")
		return
	}
	caseID, caseErr := uuid.Parse(body.CaseID)
	var incidentID *uuid.UUID
	if body.IncidentID != nil {
		parsed, err := uuid.Parse(*body.IncidentID)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_incident_id", "Valid incident ID is required")
			return
		}
		incidentID = &parsed
	}
	reason, validReason := moderationReason(body.Reason)
	if caseErr != nil || !validReason || !body.ReviewAt.After(time.Now().UTC()) || (body.Kind == "safety") != (incidentID != nil) {
		driverError(ctx, http.StatusBadRequest, "invalid_flag", "Valid linked evidence, reason, and future review date are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	active, err := lockActiveRider(ctx, tx, riderID)
	if err != nil || !active {
		driverError(ctx, http.StatusNotFound, "rider_not_found", "Active Rider was not found")
		return
	}
	linked, err := caseBelongsToRider(ctx, tx, riderID, caseID, body.Kind, incidentID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Evidence could not be checked")
		return
	}
	if !linked {
		driverError(ctx, http.StatusBadRequest, "invalid_case", "Linked case or incident must match the Rider and flag type")
		return
	}
	var existing bool
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM rider_flags WHERE rider_id=$1 AND kind=$2 AND case_id=$3 AND status='open')`, riderID, body.Kind, caseID).Scan(&existing); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be checked")
		return
	}
	if existing {
		driverError(ctx, http.StatusConflict, "flag_already_open", "An open flag already exists for this case and type")
		return
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_flags(id,rider_id,kind,case_id,incident_id,reason,review_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8::UUID)`, id, riderID, body.Kind, caseID, incidentID, reason, body.ReviewAt, currentAdmin(ctx).ID)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_flag_actions(id,flag_id,actor_admin_id,action,reason) VALUES($1,$2,$3::UUID,'created',$4)`, uuid.New(), id, currentAdmin(ctx).ID, reason)
	}
	if err == nil {
		err = insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, "rider.flag.created", "", "rider", riderID.String(), gin.H{"flagId": id.String(), "caseId": caseID.String(), "kind": body.Kind, "reason": reason}, nil, gin.H{"status": "open", "kind": body.Kind})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "riderId": riderID.String(), "kind": body.Kind, "caseId": caseID.String(), "incidentId": incidentID, "status": "open", "reviewAt": body.ReviewAt, "reason": reason}})
}

func (a *adminAPI) resolveRiderFlag(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	id, err := uuid.Parse(ctx.Param("flagID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_flag_id", "Valid flag ID is required")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_reason", "Reason is required")
		return
	}
	reason, valid := moderationReason(body.Reason)
	if !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_reason", "Reason must be 5 to 1000 characters")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be resolved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var kind, status string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT kind,status FROM rider_flags WHERE id=$1 AND rider_id=$2 FOR UPDATE`, id, riderID).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "flag_not_found", "Flag was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be read")
		return
	}
	if !canManageRiderFlag(ctx, kind) {
		driverError(ctx, http.StatusForbidden, "admin_permission_denied", "Flag type requires its owning staff role")
		return
	}
	if status != "open" {
		driverError(ctx, http.StatusConflict, "flag_already_resolved", "Flag was already resolved")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE rider_flags SET status='resolved',resolved_by=$2::UUID,resolved_at=NOW(),resolution_reason=$3 WHERE id=$1`, id, currentAdmin(ctx).ID, reason)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO rider_flag_actions(id,flag_id,actor_admin_id,action,reason) VALUES($1,$2,$3::UUID,'resolved',$4)`, uuid.New(), id, currentAdmin(ctx).ID, reason)
	}
	if err == nil {
		err = insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, "rider.flag.resolved", "", "rider", riderID.String(), gin.H{"flagId": id.String(), "kind": kind, "reason": reason}, gin.H{"status": "open", "kind": kind}, gin.H{"status": "resolved", "kind": kind})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flag could not be resolved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "riderId": riderID.String(), "kind": kind, "status": "resolved"}})
}

func (a *adminAPI) listRiderFlags(ctx *gin.Context) {
	riderID, ok := moderationRiderID(ctx)
	if !ok {
		return
	}
	if !adminHasPermission(ctx, permissionRiderFlags) {
		driverError(ctx, http.StatusForbidden, "admin_permission_denied", "Flag permission is required")
		return
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	canSupport := adminHasPermission(ctx, permissionSupportWrite)
	canSafety := adminHasPermission(ctx, permissionSafetyWrite)
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,kind,case_id::TEXT,incident_id::TEXT,status,reason,review_at,created_by::TEXT,created_at,resolved_by::TEXT,resolved_at,resolution_reason FROM rider_flags WHERE rider_id=$1 AND (($5 AND kind IN ('support','payment')) OR ($6 AND kind='safety')) AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2,$3::UUID)) ORDER BY created_at DESC,id DESC LIMIT $4`, riderID, at, id, limit+1, canSupport, canSafety)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flags are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit+1)
	ids := make([]string, 0, limit+1)
	times := make([]time.Time, 0, limit+1)
	for rows.Next() {
		var flagID, kind, caseID, status, reason, createdBy string
		var incidentID, resolvedBy, resolutionReason *string
		var reviewAt, createdAt time.Time
		var resolvedAt *time.Time
		if rows.Scan(&flagID, &kind, &caseID, &incidentID, &status, &reason, &reviewAt, &createdBy, &createdAt, &resolvedBy, &resolvedAt, &resolutionReason) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flags are unavailable")
			return
		}
		items = append(items, gin.H{"id": flagID, "riderId": riderID.String(), "kind": kind, "caseId": caseID, "incidentId": incidentID, "status": status, "reason": reason, "reviewAt": reviewAt, "createdBy": createdBy, "createdAt": createdAt, "resolvedBy": resolvedBy, "resolvedAt": resolvedAt, "resolutionReason": resolutionReason})
		ids = append(ids, flagID)
		times = append(times, createdAt)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "rider_moderation_unavailable", "Flags are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: times[limit-1], ID: ids[limit-1]})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}
