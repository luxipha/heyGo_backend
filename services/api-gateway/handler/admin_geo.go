package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"net/http"
	"strings"
)

func (a *adminAPI) getGeoSettings(c *gin.Context) {
	status, err := geo.ReadSettings(c.Request.Context(), a.pool)
	if err != nil {
		driverError(c, 503, "geo_settings_unavailable", "Geo settings unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: status})
}
func (a *adminAPI) putGeoSettings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var body struct {
		Settings geo.Settings `json:"settings"`
		Version  int64        `json:"version"`
		Reason   string       `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil || len(strings.TrimSpace(body.Reason)) < 3 || len(body.Reason) > 500 {
		driverError(c, 400, "invalid_geo_settings", "Valid settings and a change reason are required")
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	body.Settings.OSRMEndpoint = strings.TrimSpace(body.Settings.OSRMEndpoint)
	if err := body.Settings.Validate(); err != nil {
		driverError(c, 400, "invalid_geo_settings", err.Error())
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "geo_settings_unavailable", "Geo settings unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(670001)`); err != nil {
		driverError(c, 503, "geo_settings_unavailable", "Geo settings unavailable")
		return
	}
	var version int64
	if err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT version FROM admin_geo_settings WHERE id=1),0)`).Scan(&version); err != nil {
		driverError(c, 503, "geo_settings_unavailable", "Geo settings unavailable")
		return
	}
	if version != body.Version {
		driverError(c, 409, "geo_settings_changed", "Geo settings changed; reload before saving")
		return
	}
	raw, _ := json.Marshal(body.Settings)
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_geo_settings(id,configuration,version,updated_by) VALUES(1,$1::JSONB,1,$2::UUID) ON CONFLICT(id) DO UPDATE SET configuration=EXCLUDED.configuration,version=admin_geo_settings.version+1,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, raw, currentAdmin(c).ID)
	if err == nil {
		err = adminAudit(c, tx, "geo.settings.updated", "", gin.H{"settings": body.Settings, "reason": body.Reason, "previousVersion": version})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "geo_settings_unavailable", "Geo settings could not be saved")
		return
	}
	a.getGeoSettings(c)
}
