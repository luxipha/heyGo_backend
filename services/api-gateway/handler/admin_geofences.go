package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var geofenceCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)

func registerAdminGeofenceRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/geofences", a.listGeofences)
	group.GET("/geofences/:id", a.geofenceDetail)
	group.POST("/geofences", a.requireCSRF, a.createGeofence)
	group.POST("/geofences/:id/approve", a.requireCSRF, a.approveGeofence)
	group.POST("/geofences/:id/retirement-requests", a.requireCSRF, a.requestGeofenceRetirement)
	group.POST("/geofences/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approveGeofenceRetirement)
}

func geofenceID(ctx *gin.Context, param string) (string, bool) {
	id, err := uuid.Parse(ctx.Param(param))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_geofence_id", "Valid geofence ID is required")
		return "", false
	}
	return id.String(), true
}

func (a *adminAPI) listGeofences(ctx *gin.Context) {
	kind := ctx.Query("kind")
	status := ctx.DefaultQuery("status", "all")
	if kind != "" && kind != "market" && kind != "region" && kind != "airport" || status != "all" && status != "draft" && status != "approved" {
		driverError(ctx, http.StatusBadRequest, "invalid_geofence_filter", "Invalid geofence filter")
		return
	}
	cursor := ctx.Query("cursor")
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Valid cursor is required")
			return
		}
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT g.id::TEXT,g.kind,g.code,g.name,g.version,g.status,g.effective_from,g.effective_until,g.created_at
		FROM trip_geofences g
		WHERE ($1='' OR g.kind=$1) AND ($2='all' OR g.status=$2)
		AND ($3='' OR (g.created_at,g.id)<(SELECT c.created_at,c.id FROM trip_geofences c WHERE c.id=NULLIF($3,'')::UUID))
		ORDER BY g.created_at DESC,g.id DESC LIMIT 51`, kind, status, cursor)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "geofences_unavailable", "Geofences are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 50)
	for rows.Next() {
		var id, itemKind, code, name, state string
		var version int
		var from, created time.Time
		var until *time.Time
		if err := rows.Scan(&id, &itemKind, &code, &name, &version, &state, &from, &until, &created); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "geofences_unavailable", "Geofences are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "kind": itemKind, "code": code, "name": name, "version": version, "status": state, "effectiveFrom": from, "effectiveUntil": until, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "geofences_unavailable", "Geofences are unavailable")
		return
	}
	var next *string
	if len(items) > 50 {
		value := items[49]["id"].(string)
		next = &value
		items = items[:50]
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) geofenceDetail(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	var kind, code, name, state, creator string
	var approver *string
	var version int
	var from, created time.Time
	var until, approvedAt *time.Time
	var boundary json.RawMessage
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT kind,code,name,version,ST_AsGeoJSON(boundary)::JSONB,status,effective_from,effective_until,
		created_by::TEXT,approved_by::TEXT,approved_at,created_at FROM trip_geofences WHERE id=$1::UUID`, id).Scan(
		&kind, &code, &name, &version, &boundary, &state, &from, &until, &creator, &approver, &approvedAt, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "geofence_not_found", "Geofence was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "geofences_unavailable", "Geofence is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "kind": kind, "code": code, "name": name, "version": version,
		"boundary": boundary, "status": state, "effectiveFrom": from, "effectiveUntil": until,
		"createdBy": creator, "approvedBy": approver, "approvedAt": approvedAt, "createdAt": created}})
}

func (a *adminAPI) createGeofence(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1<<20)
	var body struct {
		Kind           string          `json:"kind"`
		Code           string          `json:"code"`
		Name           string          `json:"name"`
		Boundary       json.RawMessage `json:"boundary"`
		EffectiveFrom  time.Time       `json:"effectiveFrom"`
		EffectiveUntil *time.Time      `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_geofence", "Valid geofence details are required")
		return
	}
	body.Code = strings.ToUpper(strings.TrimSpace(body.Code))
	body.Name = strings.TrimSpace(body.Name)
	var shape struct {
		Type string `json:"type"`
	}
	if (body.Kind != "market" && body.Kind != "region" && body.Kind != "airport") || !geofenceCodePattern.MatchString(body.Code) ||
		body.Name == "" || len(body.Name) > 120 || body.EffectiveFrom.IsZero() ||
		(body.EffectiveUntil != nil && !body.EffectiveUntil.After(body.EffectiveFrom)) ||
		json.Unmarshal(body.Boundary, &shape) != nil || (shape.Type != "Polygon" && shape.Type != "MultiPolygon") {
		driverError(ctx, http.StatusBadRequest, "invalid_geofence", "Valid kind, code, name, dates, and GeoJSON polygon are required")
		return
	}
	var valid bool
	err := a.pool.QueryRow(ctx.Request.Context(), `WITH b AS (SELECT ST_GeomFromGeoJSON($1)::geometry AS geom)
		SELECT ST_IsValid(geom) AND NOT ST_IsEmpty(geom) AND ST_NDims(geom)=2 AND ST_XMin(geom)>=-180 AND ST_XMax(geom)<=180
		AND ST_YMin(geom)>=-90 AND ST_YMax(geom)<=90 FROM b`, string(body.Boundary)).Scan(&valid)
	if err != nil || !valid {
		driverError(ctx, http.StatusBadRequest, "invalid_geofence_boundary", "GeoJSON boundary must be a valid geographic polygon")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	lockKey := body.Kind + ":" + body.Code
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		reviewFailure(ctx)
		return
	}
	var version int
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM trip_geofences WHERE kind=$1 AND code=$2`, body.Kind, body.Code).Scan(&version); err != nil {
		reviewFailure(ctx)
		return
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,effective_until,created_by)
		VALUES($1::UUID,$2,$3,$4,$5,ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($6),4326)),'draft',$7,$8,$9::UUID)`,
		id, body.Kind, body.Code, body.Name, version, string(body.Boundary), body.EffectiveFrom.UTC(), body.EffectiveUntil, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "geofence_draft_created", "", gin.H{"geofenceId": id, "kind": body.Kind, "code": body.Code, "version": version}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "status": "draft", "version": version}})
}

func (a *adminAPI) approveGeofence(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var kind, code, state, creator string
	var from time.Time
	var until *time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT kind,code,status,created_by::TEXT,effective_from,effective_until FROM trip_geofences WHERE id=$1::UUID FOR UPDATE`, id).Scan(&kind, &code, &state, &creator, &from, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "geofence_not_found", "Geofence was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if state != "draft" || creator == currentAdmin(ctx).ID || (until != nil && !until.After(time.Now())) {
		driverError(ctx, http.StatusConflict, "geofence_approval_unavailable", "A different staff member must approve a current draft")
		return
	}
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "approve:"+kind); err != nil {
		reviewFailure(ctx)
		return
	}
	var overlap bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trip_geofences g JOIN trip_geofences d ON d.id=$1::UUID
		WHERE g.id<>d.id AND g.kind=d.kind AND g.status='approved'
		AND (g.effective_until IS NULL OR g.effective_until>d.effective_from)
		AND (d.effective_until IS NULL OR d.effective_until>g.effective_from)
		AND ST_Area(ST_Intersection(g.boundary,d.boundary))>0)`, id).Scan(&overlap)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if overlap {
		driverError(ctx, http.StatusConflict, "geofence_overlap", "An approved zone of this kind already covers part of the boundary during that period")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE trip_geofences SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID AND status='draft'`, id, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "geofence_approved", "", gin.H{"geofenceId": id, "kind": kind, "code": code}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "status": "approved"}})
}

func (a *adminAPI) requestGeofenceRetirement(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	var body struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&body) != nil || body.EffectiveUntil.Before(time.Now()) {
		driverError(ctx, http.StatusBadRequest, "invalid_retirement", "A future effectiveUntil is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	requestID := uuid.NewString()
	tag, err := tx.Exec(ctx.Request.Context(), `INSERT INTO trip_geofence_retirements(id,geofence_id,effective_until,requested_by,status)
		SELECT $1::UUID,id,$3,$4::UUID,'pending' FROM trip_geofences
		WHERE id=$2::UUID AND status='approved' AND effective_until IS NULL AND effective_from<$3`, requestID, id, body.EffectiveUntil.UTC(), currentAdmin(ctx).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Geofence cannot be retired")
		return
	}
	if adminAudit(ctx, tx, "geofence_retirement_requested", "", gin.H{"geofenceId": id, "requestId": requestID, "effectiveUntil": body.EffectiveUntil.UTC()}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": requestID, "status": "pending"}})
}

func (a *adminAPI) approveGeofenceRetirement(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	requestID, ok := geofenceID(ctx, "requestID")
	if !ok {
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var requester string
	var until time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT requested_by::TEXT,effective_until FROM trip_geofence_retirements
		WHERE id=$1::UUID AND geofence_id=$2::UUID AND status='pending' FOR UPDATE`, requestID, id).Scan(&requester, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "retirement_not_found", "Pending retirement was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if requester == currentAdmin(ctx).ID || until.Before(time.Now()) {
		driverError(ctx, http.StatusConflict, "retirement_approval_unavailable", "A different staff member must approve before the retirement time")
		return
	}
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE trip_geofences SET effective_until=$2 WHERE id=$1::UUID AND status='approved' AND effective_until IS NULL`, id, until)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Geofence cannot be retired")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE trip_geofence_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID`, requestID, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "geofence_retirement_approved", "", gin.H{"geofenceId": id, "requestId": requestID, "effectiveUntil": until}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "effectiveUntil": until}})
}
