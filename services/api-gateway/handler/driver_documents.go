package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxDriverDocumentBytes int64 = 15 << 20
const uploadLifetime = 15 * time.Minute

func validOperationalDocument(kind string) bool {
	switch kind {
	case "driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit":
		return true
	}
	return false
}

func validDocumentSide(kind, side string) bool {
	if kind == "driver_license" {
		return side == "front" || side == "back"
	}
	return side == "document"
}

func validDocumentContentType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "application/pdf":
		return true
	}
	return false
}

func (a *driverAPI) createDocumentUpload(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is not configured")
		return
	}
	var body struct {
		Type        string `json:"type"`
		Side        string `json:"side"`
		ContentType string `json:"contentType"`
		SizeBytes   int64  `json:"sizeBytes"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validOperationalDocument(body.Type) || !validDocumentSide(body.Type, body.Side) || !validDocumentContentType(body.ContentType) || body.SizeBytes <= 0 || body.SizeBytes > maxDriverDocumentBytes {
		driverError(ctx, http.StatusBadRequest, "invalid_document_upload", "Document type, side, content type, or size is invalid")
		return
	}
	if !a.ensureProfile(ctx) {
		return
	}
	uploadID := uuid.New()
	storageKey := fmt.Sprintf("driver-documents/%s/%s", driverID(ctx), uploadID.String())
	expiresAt := time.Now().UTC().Add(uploadLifetime)
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO driver_document_uploads(id,driver_id,type,side,storage_key,content_type,size_bytes,expires_at)
		VALUES($1,$2::UUID,$3,$4,$5,$6,$7,$8)`, uploadID, driverID(ctx), body.Type, body.Side, storageKey, body.ContentType, body.SizeBytes, expiresAt)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "upload_unavailable", "Document upload could not be reserved")
		return
	}
	url, headers, err := a.files.PresignPut(ctx.Request.Context(), storageKey, body.ContentType, body.SizeBytes, uploadLifetime)
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM driver_document_uploads WHERE id=$1`, uploadID)
		driverError(ctx, http.StatusServiceUnavailable, "upload_unavailable", "Document upload URL could not be created")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"uploadId": uploadID.String(), "url": url, "method": "PUT", "headers": headers, "expiresAt": expiresAt}})
}

type documentSubmission struct {
	Type      string     `json:"type"`
	UploadIDs []string   `json:"uploadIds"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

type uploadRecord struct {
	id                     uuid.UUID
	side, key, contentType string
	size                   int64
}

func (a *driverAPI) submitDocument(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is not configured")
		return
	}
	var body documentSubmission
	if ctx.ShouldBindJSON(&body) != nil || !validOperationalDocument(body.Type) || len(body.UploadIDs) == 0 || len(body.UploadIDs) > 2 || (body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now())) {
		driverError(ctx, http.StatusBadRequest, "invalid_document", "Document submission is invalid")
		return
	}
	if (body.Type == "driver_license" && len(body.UploadIDs) != 2) || (body.Type != "driver_license" && len(body.UploadIDs) != 1) {
		driverError(ctx, http.StatusBadRequest, "invalid_document_sides", "Required document sides are missing")
		return
	}
	if oldID := ctx.Param("id"); oldID != "" {
		if _, err := uuid.Parse(oldID); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_document_id", "Document ID is invalid")
			return
		}
		var currentStatus string
		err := a.pool.QueryRow(ctx.Request.Context(), `SELECT status FROM driver_documents WHERE id=$1::UUID AND driver_id=$2::UUID AND type=$3 AND id=(SELECT id FROM driver_documents WHERE driver_id=$2::UUID AND type=$3 ORDER BY submitted_at DESC,id DESC LIMIT 1)`, oldID, driverID(ctx), body.Type).Scan(&currentStatus)
		if errors.Is(err, pgx.ErrNoRows) || currentStatus != "rejected" {
			driverError(ctx, http.StatusConflict, "document_not_rejected", "Only the current rejected document can be resubmitted")
			return
		}
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be checked")
			return
		}
	}
	uploads := make([]uploadRecord, 0, len(body.UploadIDs))
	sides := map[string]bool{}
	for _, rawID := range body.UploadIDs {
		id, err := uuid.Parse(rawID)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_upload_id", "Upload ID is invalid")
			return
		}
		var item uploadRecord
		item.id = id
		var expiresAt time.Time
		var consumedAt *time.Time
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT side,storage_key,content_type,size_bytes,expires_at,consumed_at FROM driver_document_uploads
			WHERE id=$1 AND driver_id=$2::UUID AND type=$3`, id, driverID(ctx), body.Type).Scan(&item.side, &item.key, &item.contentType, &item.size, &expiresAt, &consumedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			driverError(ctx, http.StatusBadRequest, "invalid_upload", "Upload is missing, expired, already used, or duplicated")
			return
		}
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "upload_unavailable", "Upload could not be checked")
			return
		}
		if consumedAt != nil || !expiresAt.After(time.Now()) || sides[item.side] {
			driverError(ctx, http.StatusBadRequest, "invalid_upload", "Upload is missing, expired, already used, or duplicated")
			return
		}
		sides[item.side] = true
		info, err := a.files.Head(ctx.Request.Context(), item.key)
		if err != nil || info.Size != item.size || !strings.EqualFold(info.ContentType, item.contentType) {
			driverError(ctx, http.StatusBadRequest, "upload_incomplete", "Uploaded file does not match its reservation")
			return
		}
		uploads = append(uploads, item)
	}
	if body.Type == "driver_license" && (!sides["front"] || !sides["back"]) {
		driverError(ctx, http.StatusBadRequest, "invalid_document_sides", "Front and back of the driver license are required")
		return
	}
	files := make([]gin.H, 0, len(uploads))
	for _, item := range uploads {
		files = append(files, gin.H{"side": item.side, "storageKey": item.key, "contentType": item.contentType, "sizeBytes": item.size})
	}
	metadata, err := json.Marshal(gin.H{"files": files})
	if err != nil {
		driverError(ctx, http.StatusInternalServerError, "document_encoding_failed", "Document could not be submitted")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	for _, item := range uploads {
		result, err := tx.Exec(ctx.Request.Context(), `UPDATE driver_document_uploads SET consumed_at=NOW() WHERE id=$1 AND driver_id=$2::UUID AND consumed_at IS NULL AND expires_at>NOW()`, item.id, driverID(ctx))
		if err != nil || result.RowsAffected() != 1 {
			driverError(ctx, http.StatusConflict, "upload_already_used", "Upload has already been used")
			return
		}
	}
	documentID := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata,expires_at)
		VALUES($1,$2::UUID,$3,'under_review',$4,$5::JSONB,$6)`, documentID, driverID(ctx), body.Type, uploads[0].key, metadata, body.ExpiresAt)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	_, err = insertDriverEvent(ctx, tx, driverID(ctx), "driver.document.updated", gin.H{"id": documentID.String(), "type": body.Type, "status": "under_review"})
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	if err := tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_unavailable", "Document could not be submitted")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": documentID.String(), "type": body.Type, "status": "under_review"}})
}
