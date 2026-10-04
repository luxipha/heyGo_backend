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

const legalNamePurpose = "heygo-driver-verified-legal-name-v1"

func (a *adminAPI) recordVerifiedLegalName(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		LegalName  string `json:"legalName"`
		DocumentID string `json:"documentId"`
		Reason     string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_legal_name", "Legal name, approved Driver licence, and reason are required")
		return
	}
	name, reason := strings.Join(strings.Fields(body.LegalName), " "), strings.TrimSpace(body.Reason)
	document, err := uuid.Parse(body.DocumentID)
	if err != nil || len([]rune(name)) < 3 || len([]rune(name)) > 160 || len([]rune(reason)) < 5 || len([]rune(reason)) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_legal_name", "Valid legal name, approved Driver licence, and reason are required")
		return
	}
	if len(a.settingsKey) != 32 {
		driverError(ctx, http.StatusServiceUnavailable, "name_encryption_unavailable", "Verified name storage is unavailable")
		return
	}
	sealed, err := sealAdminValue(a.settingsKey, name, legalNamePurpose)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "name_encryption_unavailable", "Verified name storage is unavailable")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var valid bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles p JOIN driver_documents d ON d.driver_id=p.driver_id
		WHERE p.driver_id=$1::UUID AND p.manual_verification_status='approved'
		AND d.id=$2 AND d.type='driver_license' AND d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()))`, driver, document).Scan(&valid)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if !valid {
		driverError(ctx, http.StatusConflict, "identity_evidence_required", "Approved identity review and current Driver licence are required")
		return
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_verified_legal_names(id,driver_id,evidence_document_id,name_ciphertext,reviewed_by)
		VALUES($1,$2::UUID,$3,$4,$5::UUID)`, id, driver, document, sealed, currentAdmin(ctx).ID)
	if err == nil {
		err = adminAudit(ctx, tx, "driver.verified_legal_name.recorded", driver, gin.H{"recordId": id.String(), "documentId": document.String(), "reason": reason})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "driverId": driver, "documentId": document.String(), "reviewedBy": currentAdmin(ctx).ID}})
}

func (a *adminAPI) verifiedLegalName(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var id, document, reviewer string
	var sealed []byte
	var created time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,evidence_document_id::TEXT,reviewed_by::TEXT,name_ciphertext,created_at
		FROM driver_verified_legal_names WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, driver).Scan(&id, &document, &reviewer, &sealed, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: nil})
		return
	}
	if err != nil || len(a.settingsKey) != 32 {
		driverError(ctx, http.StatusServiceUnavailable, "verified_name_unavailable", "Verified name is unavailable")
		return
	}
	name, err := openAdminValue(a.settingsKey, sealed, legalNamePurpose)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "verified_name_unavailable", "Verified name is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "driverId": driver, "legalName": name, "documentId": document, "reviewedBy": reviewer, "reviewedAt": created}})
}
