package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/zonepricing"
	"math"
)

// Validate and snapshot approved, contained, non-overlapping pickup-zone geometries.
func writePricingZones(c *gin.Context, tx pgx.Tx, id string, rules []zonepricing.Rule) error {
	if len(rules) > 100 {
		return errors.New("At most 100 pickup zones are allowed")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if _, err := uuid.Parse(r.ZoneID); err != nil || !zonepricing.Valid(r) || seen[r.ZoneID] {
			return errors.New("Unique approved zones and valid adjustments are required")
		}
		seen[r.ZoneID] = true
		tag, err := tx.Exec(c.Request.Context(), `INSERT INTO pricing_zone_rules(pricing_id,zone_id,zone_name,boundary,kind,value)
 SELECT p.id,z.id,z.name,z.boundary,$3,$4 FROM pricing_versions p JOIN trip_geofences m ON m.kind='market' AND m.code=p.market_code AND m.status='approved'
 JOIN trip_geofences z ON z.id=$2::uuid AND z.kind IN ('region','airport') AND z.status='approved'
 WHERE p.id=$1::uuid AND m.effective_from<=p.effective_from AND (m.effective_until IS NULL OR (p.effective_until IS NOT NULL AND m.effective_until>=p.effective_until))
 AND z.effective_from<=p.effective_from AND (z.effective_until IS NULL OR (p.effective_until IS NOT NULL AND z.effective_until>=p.effective_until))
 AND ST_CoveredBy(z.boundary,m.boundary)
 AND NOT EXISTS(SELECT 1 FROM pricing_zone_rules x WHERE x.pricing_id=p.id AND ST_Intersects(x.boundary,z.boundary))`, id, r.ZoneID, r.Kind, r.Value)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Zones must be approved, within the market, cover the pricing period and not overlap")
		}
	}
	return nil
}
func (a *adminAPI) readPricingZones(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID required")
		return
	}
	var revision int64
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT zone_revision FROM pricing_versions WHERE id=$1`, id).Scan(&revision); err != nil {
		driverError(c, 404, "pricing_not_found", "Pricing not found")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT zone_id::text,zone_name,kind,value FROM pricing_zone_rules WHERE pricing_id=$1 ORDER BY zone_name`, id)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	rules := []zonepricing.Rule{}
	for rows.Next() {
		r := zonepricing.Rule{}
		if rows.Scan(&r.ZoneID, &r.ZoneName, &r.Kind, &r.Value) != nil {
			reviewFailure(c)
			return
		}
		rules = append(rules, r)
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"revision": revision, "rules": rules}})
}
func (a *adminAPI) savePricingZones(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID required")
		return
	}
	var body struct {
		Revision int64              `json:"revision"`
		Rules    []zonepricing.Rule `json:"rules"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Rules == nil || body.Revision < 0 {
		driverError(c, 400, "invalid_zone_rules", "Rules and revision required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var state, creator string
	var revision int64
	err = tx.QueryRow(c.Request.Context(), `SELECT status,created_by::text,zone_revision FROM pricing_versions WHERE id=$1 FOR UPDATE`, id).Scan(&state, &creator, &revision)
	if err != nil || state != "draft" || creator != currentAdmin(c).ID || revision != body.Revision {
		driverError(c, 409, "pricing_locked", "Only the creator can edit the latest unpublished pricing draft")
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM pricing_zone_rules WHERE pricing_id=$1`, id); err != nil {
		reviewFailure(c)
		return
	}
	if err = writePricingZones(c, tx, id.String(), body.Rules); err != nil {
		driverError(c, 400, "invalid_zone_rules", err.Error())
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE pricing_versions SET zone_revision=zone_revision+1 WHERE id=$1`, id); err != nil {
		reviewFailure(c)
		return
	}
	if adminAudit(c, tx, "pricing.zones.saved", "", gin.H{"policyId": id.String(), "rules": body.Rules}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"revision": revision + 1, "rules": body.Rules}})
}
func (a *adminAPI) simulationZone(c *gin.Context, id string, lat, lng *float64) (*zonepricing.Rule, bool) {
	if (lat == nil) != (lng == nil) || (lat != nil && (math.IsNaN(*lat) || math.IsNaN(*lng) || math.IsInf(*lat, 0) || math.IsInf(*lng, 0) || *lat < -90 || *lat > 90 || *lng < -180 || *lng > 180)) {
		driverError(c, 400, "invalid_pickup", "Valid pickup coordinates required")
		return nil, false
	}
	if lat == nil {
		return nil, true
	}
	r, err := zonepricing.Match(c.Request.Context(), a.pool, id, *lat, *lng)
	if err != nil {
		driverError(c, 409, "zone_pricing_unavailable", "Zone pricing could not be resolved")
		return nil, false
	}
	return r, true
}
