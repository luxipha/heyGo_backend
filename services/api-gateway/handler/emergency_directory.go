package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"strings"
)

type emergencyDirectoryContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

func (a *adminAPI) getEmergencyDirectory(c *gin.Context) {
	var raw []byte
	var version int64
	if err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT contacts FROM admin_emergency_directory WHERE id=1),'[]'::JSONB),COALESCE((SELECT version FROM admin_emergency_directory WHERE id=1),0)`).Scan(&raw, &version); err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"contacts": json.RawMessage(raw), "version": version}})
}
func (a *adminAPI) putEmergencyDirectory(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	var body struct {
		Contacts []emergencyDirectoryContact `json:"contacts"`
		Version  int64                       `json:"version"`
		Reason   string                      `json:"reason"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Contacts) > 20 || len(strings.TrimSpace(body.Reason)) < 3 || len(body.Reason) > 500 {
		driverError(c, 400, "invalid_contacts", "Provide up to 20 named contacts and a change reason")
		return
	}
	if body.Contacts == nil {
		body.Contacts = []emergencyDirectoryContact{}
	}
	for i := range body.Contacts {
		v := &body.Contacts[i]
		v.Name = strings.TrimSpace(v.Name)
		v.Phone = strings.TrimSpace(v.Phone)
		if v.Name == "" || len([]rune(v.Name)) > 120 || !regexpEmergencyPhone(v.Phone) {
			driverError(c, 400, "invalid_contacts", "Each contact needs a name and a phone number (international format or a short emergency number)")
			return
		}
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(710001)`)
	var version int64
	if err == nil {
		err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT version FROM admin_emergency_directory WHERE id=1),0)`).Scan(&version)
	}
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	if version != body.Version {
		driverError(c, 409, "directory_changed", "Reload before saving")
		return
	}
	raw, _ := json.Marshal(body.Contacts)
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_emergency_directory(id,contacts) VALUES(1,$1::JSONB) ON CONFLICT(id) DO UPDATE SET contacts=EXCLUDED.contacts,version=admin_emergency_directory.version+1,updated_at=NOW()`, string(raw))
	if err == nil {
		err = adminAudit(c, tx, "emergency.directory.updated", "", gin.H{"reason": body.Reason, "contactCount": len(body.Contacts), "previousVersion": version})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts could not be saved")
		return
	}
	a.getEmergencyDirectory(c)
}
func regexpEmergencyPhone(phone string) bool {
	if e164PhonePattern.MatchString(phone) {
		return true
	}
	if len(phone) < 3 || len(phone) > 6 {
		return false
	}
	for _, r := range phone {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (a *driverAPI) safetyDirectory(c *gin.Context) {
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::TEXT,full_name,relationship,phone_e164 FROM driver_additional_safety_contacts WHERE driver_id=$1::UUID ORDER BY created_at,id`, driverID(c))
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	defer rows.Close()
	personal := []gin.H{}
	for rows.Next() {
		var id, name, relationship, phone string
		if rows.Scan(&id, &name, &relationship, &phone) != nil {
			driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
			return
		}
		personal = append(personal, gin.H{"id": id, "fullName": name, "relationship": relationship, "phoneNumber": phone})
	}
	if rows.Err() != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	rows.Close()
	var raw []byte
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT contacts FROM admin_emergency_directory WHERE id=1),'[]'::JSONB)`).Scan(&raw); err != nil {
		driverError(c, 503, "directory_unavailable", "Emergency contacts unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"personal": personal, "emergency": json.RawMessage(raw)}})
}
func (a *driverAPI) addSafetyContact(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var body struct {
		FullName     string `json:"fullName"`
		Relationship string `json:"relationship"`
		Phone        string `json:"phoneNumber"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_contact", "Invalid contact")
		return
	}
	body.FullName = strings.TrimSpace(body.FullName)
	body.Relationship = strings.TrimSpace(body.Relationship)
	phone, valid := normalizeSafetyContactPhone(body.Phone)
	if !valid || body.FullName == "" || len([]rune(body.FullName)) > 120 || body.Relationship == "" || len([]rune(body.Relationship)) > 80 {
		driverError(c, 400, "invalid_contact", "Provide a name, relationship and valid phone number")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Contact could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT id FROM users WHERE id=$1::UUID FOR UPDATE`, driverID(c))
	var count int
	if err == nil {
		err = tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM driver_additional_safety_contacts WHERE driver_id=$1::UUID`, driverID(c)).Scan(&count)
	}
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Contact could not be saved")
		return
	}
	if count >= 9 {
		driverError(c, 400, "contact_limit", "You can add up to nine additional contacts")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_additional_safety_contacts(id,driver_id,full_name,relationship,phone_e164) VALUES($1,$2::UUID,$3,$4,$5)`, uuid.New(), driverID(c), body.FullName, body.Relationship, phone)
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Contact could not be saved")
		return
	}
	a.safetyDirectory(c)
}
func (a *driverAPI) removeSafetyContact(c *gin.Context) {
	id, err := uuid.Parse(c.Param("contactID"))
	if err != nil {
		driverError(c, 400, "invalid_contact", "Invalid contact")
		return
	}
	tag, err := a.pool.Exec(c.Request.Context(), `DELETE FROM driver_additional_safety_contacts WHERE id=$1 AND driver_id=$2::UUID`, id, driverID(c))
	if err != nil {
		driverError(c, 503, "directory_unavailable", "Contact could not be removed")
		return
	}
	if tag.RowsAffected() == 0 {
		driverError(c, 404, "contact_not_found", "Contact not found")
		return
	}
	c.Status(204)
}
