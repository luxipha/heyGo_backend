package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *adminAPI) updateDriverIdentity(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body identityProfile
	if ctx.ShouldBindJSON(&body) != nil || !normalizeIdentityProfile(&body) {
		driverError(ctx, http.StatusBadRequest, "invalid_identity", "Identity details are invalid")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var exists bool
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driver).Scan(&exists); err != nil || !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	if err = upsertIdentityProfile(ctx, tx, driver, body); err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET manual_verification_status='pending',manual_verification_reason='',manual_verified_by=NULL,manual_verified_at=NULL,admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driver)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "driver.identity.updated_by_admin", driver, gin.H{"fields": []string{"firstName", "lastName", "residentialAddress", "dateOfBirth", "age", "ninLast4", "bvnLast4", "idType", "idNumberLast4", "nextOfKin", "relationship", "emergencyNumber"}})
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driver, "driver.identity.updated", gin.H{"status": "under_review"})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": driver, "identity": body, "manualVerificationStatus": "pending", "adminStatus": "pending"}})
}
