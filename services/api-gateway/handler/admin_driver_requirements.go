package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// configurableRequirements lists, in checklist order, the items an admin can
// switch. Identity and the driver's licence are always required.
var configurableRequirements = []string{"vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit", "vehicle_inspection"}

type requirementConfigItem struct {
	Type          string     `json:"type"`
	Mode          string     `json:"mode"`
	GraceDays     int        `json:"graceDays"`
	RequiredSince *time.Time `json:"requiredSince"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	// DriversWithout counts active drivers who do not yet have it approved:
	// the people a switch to Required would affect.
	DriversWithout int `json:"driversWithout"`
}

func validRequirementMode(mode string) bool {
	return mode == "required" || mode == "optional" || mode == "off"
}

func validRequirementType(kind string) bool {
	for _, known := range configurableRequirements {
		if known == kind {
			return true
		}
	}
	return false
}

// withoutRequirementSQL counts active drivers with no approved, unexpired item.
const withoutDocumentSQL = `SELECT COUNT(*) FROM users u WHERE u.active AND u.roles @> ARRAY['driver'] AND NOT COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) FROM driver_documents_records d WHERE d.driver_id=u.id AND d.type=$1 ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE)`
const withoutInspectionSQL = `SELECT COUNT(*) FROM users u WHERE u.active AND u.roles @> ARRAY['driver'] AND NOT EXISTS(SELECT 1 FROM driver_inspections_records i JOIN driver_vehicle_records v ON v.id=i.vehicle_id WHERE v.driver_id=u.id AND i.status='completed')`

func (a *adminAPI) driverRequirements(ctx *gin.Context) {
	c := ctx.Request.Context()
	rows, err := a.pool.Query(c, `SELECT type,mode,grace_days,required_since,updated_at FROM driver_requirement_config`)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements are unavailable")
		return
	}
	byType := map[string]requirementConfigItem{}
	for rows.Next() {
		var item requirementConfigItem
		if err := rows.Scan(&item.Type, &item.Mode, &item.GraceDays, &item.RequiredSince, &item.UpdatedAt); err != nil {
			rows.Close()
			driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements are unavailable")
			return
		}
		byType[item.Type] = item
	}
	rows.Close()
	items := make([]requirementConfigItem, 0, len(configurableRequirements))
	for _, kind := range configurableRequirements {
		item, ok := byType[kind]
		if !ok {
			continue
		}
		query, args := withoutDocumentSQL, []any{kind}
		if kind == "vehicle_inspection" {
			query, args = withoutInspectionSQL, nil
		}
		if err := a.pool.QueryRow(c, query, args...).Scan(&item.DriversWithout); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements are unavailable")
			return
		}
		items = append(items, item)
	}
	var drivers int
	if err := a.pool.QueryRow(c, `SELECT COUNT(*) FROM users WHERE active AND roles @> ARRAY['driver']`).Scan(&drivers); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items, "activeDrivers": drivers}})
}

func (a *adminAPI) saveDriverRequirement(ctx *gin.Context) {
	kind := ctx.Param("type")
	var body struct {
		Mode      string `json:"mode"`
		GraceDays *int   `json:"graceDays"`
	}
	if !validRequirementType(kind) || ctx.ShouldBindJSON(&body) != nil || !validRequirementMode(body.Mode) ||
		(body.GraceDays != nil && (*body.GraceDays < 0 || *body.GraceDays > 365)) {
		driverError(ctx, http.StatusBadRequest, "invalid_requirement", "Choose Required, Optional or Off, and a grace period of 0–365 days")
		return
	}
	c := ctx.Request.Context()
	tx, err := a.pool.Begin(c)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements could not be saved")
		return
	}
	defer tx.Rollback(c)
	var before requirementConfigItem
	err = tx.QueryRow(c, `SELECT type,mode,grace_days,required_since,updated_at FROM driver_requirement_config WHERE type=$1 FOR UPDATE`, kind).
		Scan(&before.Type, &before.Mode, &before.GraceDays, &before.RequiredSince, &before.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "requirement_not_found", "Requirement was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements could not be saved")
		return
	}
	grace := before.GraceDays
	if body.GraceDays != nil {
		grace = *body.GraceDays
	}
	if body.Mode == before.Mode && grace == before.GraceDays {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"type": kind, "mode": before.Mode, "graceDays": grace, "requiredSince": before.RequiredSince, "unchanged": true}})
		return
	}
	// required_since restarts the grace window each time the item becomes
	// Required, and clears when it stops being Required.
	var after requirementConfigItem
	err = tx.QueryRow(c, `UPDATE driver_requirement_config SET mode=$2,grace_days=$3,
		required_since=CASE WHEN $2='required' AND mode<>'required' THEN NOW() WHEN $2<>'required' THEN NULL ELSE required_since END,
		updated_by=$4::UUID,updated_at=NOW() WHERE type=$1
		RETURNING type,mode,grace_days,required_since,updated_at`, kind, body.Mode, grace, currentAdmin(ctx).ID).
		Scan(&after.Type, &after.Mode, &after.GraceDays, &after.RequiredSince, &after.UpdatedAt)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements could not be saved")
		return
	}
	if err = adminAudit(ctx, tx, "driver.requirement.config", "", gin.H{
		"type":   kind,
		"before": gin.H{"mode": before.Mode, "graceDays": before.GraceDays},
		"after":  gin.H{"mode": after.Mode, "graceDays": after.GraceDays},
	}); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements could not be saved")
		return
	}
	if err = tx.Commit(c); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "requirements_unavailable", "Requirements could not be saved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"type": kind, "mode": after.Mode, "graceDays": after.GraceDays, "requiredSince": after.RequiredSince}})
}
