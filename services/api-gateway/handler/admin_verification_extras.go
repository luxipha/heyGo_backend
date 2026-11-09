package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// latestIdentityDocsSQL resolves the current selfie and ID photo submissions.
const latestIdentityDocsSQL = `(SELECT id FROM driver_documents WHERE driver_id=$1::UUID AND type='selfie_with_id' ORDER BY submitted_at DESC,id DESC LIMIT 1),
	(SELECT id FROM driver_documents WHERE driver_id=$1::UUID AND type='id_document' ORDER BY submitted_at DESC,id DESC LIMIT 1)`

// verificationExtras returns when the driver applied and the staff face-match
// answer, which reads "not_reviewed" once either photo has been replaced.
func (a *adminAPI) verificationExtras(ctx *gin.Context, driver string) (appliedAt time.Time, faceMatch string, err error) {
	err = a.vehicleDB(ctx).QueryRow(ctx.Request.Context(), `SELECT p.created_at,
		CASE WHEN f.driver_id IS NOT NULL AND f.selfie_document_id=s.id AND f.id_document_id=i.id THEN f.result ELSE 'not_reviewed' END
		FROM driver_profiles p
		LEFT JOIN LATERAL (SELECT id FROM driver_documents WHERE driver_id=p.driver_id AND type='selfie_with_id' ORDER BY submitted_at DESC,id DESC LIMIT 1) s ON TRUE
		LEFT JOIN LATERAL (SELECT id FROM driver_documents WHERE driver_id=p.driver_id AND type='id_document' ORDER BY submitted_at DESC,id DESC LIMIT 1) i ON TRUE
		LEFT JOIN driver_face_match_reviews f ON f.driver_id=p.driver_id
		WHERE p.driver_id=$1::UUID`, driver).Scan(&appliedAt, &faceMatch)
	return
}

func (a *adminAPI) recordFaceMatch(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		Result string `json:"result"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Result != "match" && body.Result != "no_match" && body.Result != "unsure") {
		driverError(ctx, http.StatusBadRequest, "invalid_face_match", "Result must be match, no_match or unsure")
		return
	}
	tx, err := a.vehicleDB(ctx).Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var selfie, idPhoto *string
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT `+latestIdentityDocsSQL, driver).Scan(&selfie, &idPhoto); err != nil {
		reviewFailure(ctx)
		return
	}
	if selfie == nil || idPhoto == nil {
		driverError(ctx, http.StatusConflict, "identity_photos_missing", "The selfie and ID photo must both be uploaded first")
		return
	}
	if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_face_match_reviews(driver_id,result,selfie_document_id,id_document_id,reviewed_by) VALUES($1::UUID,$2,$3::UUID,$4::UUID,$5::UUID)
		ON CONFLICT(driver_id) DO UPDATE SET result=EXCLUDED.result,selfie_document_id=EXCLUDED.selfie_document_id,id_document_id=EXCLUDED.id_document_id,reviewed_by=EXCLUDED.reviewed_by,reviewed_at=NOW()`,
		driver, body.Result, *selfie, *idPhoto, currentAdmin(ctx).ID); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = adminAudit(ctx, tx, "identity.face_match", driver, gin.H{"result": body.Result, "selfieDocumentId": *selfie, "idDocumentId": *idPhoto}); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"faceMatch": body.Result}})
}

// requestDocumentReupload sends the current submission of a document back to
// the driver. It is a rejection marked as a reupload request, so the Driver app
// shows "Tap to resubmit" and the driver gets a push asking for a new upload.
func (a *adminAPI) requestDocumentReupload(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	doc, ok := reviewID(ctx, "documentID")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_reupload", "Tell the driver what to upload again")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	tx, err := a.vehicleDB(ctx).Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var kind string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT d.type FROM driver_documents d WHERE d.id=$1::UUID AND d.driver_id=$2::UUID AND d.status IN ('under_review','approved') AND d.id=(SELECT d2.id FROM driver_documents d2 WHERE d2.driver_id=$2::UUID AND d2.type=d.type ORDER BY d2.submitted_at DESC,d2.id DESC LIMIT 1) FOR UPDATE`, doc, driver).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "document_not_current", "Only the current submitted or approved document can be sent back")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.Set("vehicleEvidence", true)
	if kind != "vehicle_license" && kind != "roadworthiness" && kind != "auto_insurance" && kind != "hackney_permit" {
		ctx.Set("sharedDriverEvidence", true)
	}
	if _, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_documents SET status='rejected',rejection_reason=$1,reviewed_at=NOW(),reviewed_by=$2::UUID,updated_at=NOW(),metadata=COALESCE(metadata,'{}'::jsonb)||'{"reuploadRequested":true}'::jsonb WHERE id=$3::UUID`, reason, currentAdmin(ctx).ID, doc); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = invalidateAdminApproval(ctx, tx, driver); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = adminAudit(ctx, tx, "document.reupload_requested", driver, gin.H{"documentId": doc, "type": kind, "reason": reason}); err != nil {
		reviewFailure(ctx)
		return
	}
	if _, err = insertDriverEvent(ctx, tx, driver, "driver.document.updated", gin.H{"id": doc, "type": kind, "status": "rejected", "reason": reason, "reupload": true}); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": doc, "status": "rejected", "reuploadRequested": true}})
}
