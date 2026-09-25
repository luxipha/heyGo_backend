package handler

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

var e164PhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

type driverSafetyContact struct {
	FullName     string    `json:"fullName"`
	Relationship string    `json:"relationship"`
	PhoneNumber  string    `json:"phoneNumber"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func registerDriverSafetyContactRoutes(group *gin.RouterGroup, api *driverAPI) {
	group.GET("/safety-contacts", api.getSafetyContact)
	group.PUT("/safety-contacts", api.putSafetyContact)
}

func normalizeSafetyContactPhone(value string) (string, bool) {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(value)
	if len(value) == 11 && strings.HasPrefix(value, "0") {
		value = "+234" + value[1:]
	}
	if strings.HasPrefix(value, "00") {
		value = "+" + strings.TrimPrefix(value, "00")
	}
	if !e164PhonePattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func (a *driverAPI) getSafetyContact(ctx *gin.Context) {
	var contact driverSafetyContact
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT full_name,relationship,phone_e164,updated_at
		FROM driver_safety_contacts WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&contact.FullName, &contact.Relationship, &contact.PhoneNumber, &contact.UpdatedAt)
	if err == pgx.ErrNoRows {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"contact": nil}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_contact_unavailable", "Safety contact is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"contact": contact}})
}

func (a *driverAPI) putSafetyContact(ctx *gin.Context) {
	var body struct {
		FullName     string `json:"fullName"`
		Relationship string `json:"relationship"`
		PhoneNumber  string `json:"phoneNumber"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_safety_contact", "Valid full name, relationship, and phone number are required")
		return
	}
	body.FullName = strings.TrimSpace(body.FullName)
	body.Relationship = strings.TrimSpace(body.Relationship)
	phone, ok := normalizeSafetyContactPhone(body.PhoneNumber)
	if body.FullName == "" || len([]rune(body.FullName)) > 120 || body.Relationship == "" || len([]rune(body.Relationship)) > 80 || !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_safety_contact", "Full name, relationship, and an international phone number are required")
		return
	}
	var contact driverSafetyContact
	err := a.pool.QueryRow(ctx.Request.Context(), `INSERT INTO driver_safety_contacts(driver_id,full_name,relationship,phone_e164)
		VALUES($1::UUID,$2,$3,$4)
		ON CONFLICT(driver_id) DO UPDATE SET full_name=EXCLUDED.full_name,relationship=EXCLUDED.relationship,phone_e164=EXCLUDED.phone_e164,updated_at=NOW()
		RETURNING full_name,relationship,phone_e164,updated_at`, driverID(ctx), body.FullName, body.Relationship, phone).Scan(&contact.FullName, &contact.Relationship, &contact.PhoneNumber, &contact.UpdatedAt)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "safety_contact_unavailable", "Safety contact could not be saved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"contact": contact}})
}
