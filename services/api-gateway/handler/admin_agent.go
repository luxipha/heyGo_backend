package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/agentsettings"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"strings"
)

func (a *adminAPI) getAgentSettings(c *gin.Context) {
	s, e := agentsettings.Read(c.Request.Context(), a.pool)
	if e != nil {
		driverError(c, 503, "agent_settings_unavailable", "Agent settings unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: s})
}
func (a *adminAPI) putAgentSettings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var body struct {
		Settings agentsettings.Settings `json:"settings"`
		Version  int64                  `json:"version"`
		Reason   string                 `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Settings.Validate() != nil || len(strings.TrimSpace(body.Reason)) < 3 || len(body.Reason) > 500 {
		driverError(c, 400, "invalid_agent_settings", "Valid settings and reason required")
		return
	}
	// Model/base validation uses the same provider allowlists; no request is sent.
	// Missing deploy-time secret is allowed while disabled, but never disclosed.
	if body.Settings.Provider == "qwen" && !agent.ValidQwenConfiguration(body.Settings.Model, body.Settings.BaseURL) {
		driverError(c, 400, "invalid_agent_settings", "Invalid Qwen model or regional endpoint")
		return
	}
	if body.Settings.Provider == "gemini" && !agent.ValidGeminiModel(body.Settings.Model) {
		driverError(c, 400, "invalid_agent_settings", "Invalid Gemini model")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		driverError(c, 503, "agent_settings_unavailable", "Settings unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, e = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(670002)`)
	var version int64
	if e == nil {
		e = tx.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT version FROM admin_agent_settings WHERE id=1),0)`).Scan(&version)
	}
	if e != nil {
		driverError(c, 503, "agent_settings_unavailable", "Settings unavailable")
		return
	}
	if version != body.Version {
		driverError(c, 409, "agent_settings_changed", "Reload settings before saving")
		return
	}
	raw, _ := json.Marshal(body.Settings)
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO admin_agent_settings(id,configuration,version,updated_by) VALUES(1,$1::jsonb,1,$2::uuid) ON CONFLICT(id) DO UPDATE SET configuration=EXCLUDED.configuration,version=admin_agent_settings.version+1,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, raw, currentAdmin(c).ID)
	if e == nil {
		e = adminAudit(c, tx, "agent.settings.updated", "", gin.H{"settings": body.Settings, "reason": strings.TrimSpace(body.Reason), "previousVersion": version})
	}
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		driverError(c, 503, "agent_settings_unavailable", "Settings could not be saved")
		return
	}
	a.getAgentSettings(c)
}
func (a *adminAPI) testAgentSettings(c *gin.Context) {
	reply, e := callRideAgent(c.Request.Context(), agent.Request{SchemaVersion: 1, Message: "Ikeja City Mall Lagos"})
	if e != nil {
		driverError(c, 503, "agent_connection_unavailable", "Agent connection unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"connected": true, "planner": reply.Planner, "state": reply.State, "aiAccepted": reply.Planner == "llm", "confirmationRequired": reply.ConfirmationRequired}})
}
