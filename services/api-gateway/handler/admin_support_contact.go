package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"regexp"
	"strings"
)

var supportPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func (a *adminAPI) getSupportContact(c *gin.Context) {
	var phone string
	var version int64
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT phone FROM admin_support_contact WHERE id=1),''),COALESCE((SELECT version FROM admin_support_contact WHERE id=1),0)`).Scan(&phone, &version)
	if err != nil {
		driverError(c, 503, "support_contact_unavailable", "Support contact unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"phone": phone, "version": version}})
}
func (a *adminAPI) putSupportContact(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var body struct {
		Phone   string `json:"phone"`
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_support_contact", "Invalid support contact")
		return
	}
	body.Phone = strings.TrimSpace(body.Phone)
	body.Reason = strings.TrimSpace(body.Reason)
	if (body.Phone != "" && !supportPhonePattern.MatchString(body.Phone)) || len(body.Reason) < 3 || len(body.Reason) > 500 {
		driverError(c, 400, "invalid_support_contact", "Use an international phone number and a change reason")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "support_contact_unavailable", "Support contact unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(690001)`)
	var version int64
	if err == nil {
		err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT version FROM admin_support_contact WHERE id=1),0)`).Scan(&version)
	}
	if err != nil {
		driverError(c, 503, "support_contact_unavailable", "Support contact unavailable")
		return
	}
	if version != body.Version {
		driverError(c, 409, "support_contact_changed", "Reload before saving")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_support_contact(id,phone) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET phone=EXCLUDED.phone,version=admin_support_contact.version+1,updated_at=NOW()`, body.Phone)
	if err == nil {
		err = adminAudit(c, tx, "support.contact.updated", "", gin.H{"phone": body.Phone, "reason": body.Reason, "previousVersion": version})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "support_contact_unavailable", "Support contact could not be saved")
		return
	}
	a.getSupportContact(c)
}
