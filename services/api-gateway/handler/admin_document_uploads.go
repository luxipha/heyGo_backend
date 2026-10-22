package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *adminAPI) createDocumentUpload(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
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
	var exists bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driver).Scan(&exists); err != nil || !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	id := uuid.New()
	key := fmt.Sprintf("admin-driver-documents/%s/%s", driver, id)
	expires := time.Now().UTC().Add(uploadLifetime)
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_document_uploads(id,admin_id,driver_id,type,side,storage_key,content_type,size_bytes,expires_at) VALUES($1,$2::UUID,$3::UUID,$4,$5,$6,$7,$8,$9)`, id, currentAdmin(ctx).ID, driver, body.Type, body.Side, key, body.ContentType, body.SizeBytes, expires)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	url, headers, err := a.files.PresignPut(ctx.Request.Context(), key, body.ContentType, body.SizeBytes, uploadLifetime)
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM admin_document_uploads WHERE id=$1`, id)
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"uploadId": id.String(), "url": url, "method": "PUT", "headers": headers, "expiresAt": expires}})
}

func (a *adminAPI) submitDocument(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		Type             string            `json:"type"`
		SourceDocumentID string            `json:"sourceDocumentId"`
		UploadIDs        []string          `json:"uploadIds"`
		ExpiresAt        *time.Time        `json:"expiresAt"`
		Fields           map[string]string `json:"fields"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validOperationalDocument(body.Type) || (len(body.UploadIDs) == 0 && body.SourceDocumentID == "") || len(body.UploadIDs) > 2 || (body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now())) {
		driverError(ctx, http.StatusBadRequest, "invalid_document", "Document submission is invalid")
		return
	}
	if body.SourceDocumentID == "" && ((body.Type == "driver_license" && len(body.UploadIDs) != 2) || (body.Type != "driver_license" && len(body.UploadIDs) != 1)) {
		driverError(ctx, http.StatusBadRequest, "invalid_document_sides", "Required document sides are missing")
		return
	}
	if len(body.Fields) > 12 {
		driverError(ctx, http.StatusBadRequest, "invalid_document_fields", "Document details are invalid")
		return
	}
	for key, value := range body.Fields {
		if len(key) > 64 || len(strings.TrimSpace(value)) > 200 {
			driverError(ctx, http.StatusBadRequest, "invalid_document_fields", "Document details are invalid")
			return
		}
	}
	type upload struct {
		id                     uuid.UUID
		side, key, contentType string
		size                   int64
	}
	uploads := make([]upload, 0, len(body.UploadIDs))
	sides := map[string]bool{}
	for _, rawID := range body.UploadIDs {
		id, err := uuid.Parse(rawID)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_upload_id", "Upload ID is invalid")
			return
		}
		var item upload
		var expires time.Time
		var consumed *time.Time
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT side,storage_key,content_type,size_bytes,expires_at,consumed_at FROM admin_document_uploads WHERE id=$1 AND admin_id=$2::UUID AND driver_id=$3::UUID AND type=$4`, id, currentAdmin(ctx).ID, driver, body.Type).Scan(&item.side, &item.key, &item.contentType, &item.size, &expires, &consumed)
		if errors.Is(err, pgx.ErrNoRows) || consumed != nil || !expires.After(time.Now()) || sides[item.side] {
			driverError(ctx, http.StatusBadRequest, "invalid_upload", "Document upload is missing, expired, or already used")
			return
		}
		if err != nil {
			reviewFailure(ctx)
			return
		}
		item.id = id
		sides[item.side] = true
		info, err := a.files.Head(ctx.Request.Context(), item.key)
		if err != nil || info.Size != item.size || !strings.EqualFold(info.ContentType, item.contentType) {
			driverError(ctx, http.StatusBadRequest, "upload_incomplete", "Document file does not match its reservation")
			return
		}
		uploads = append(uploads, item)
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	files := []documentEvidenceFile{}
	if body.SourceDocumentID != "" {
		if _, err := uuid.Parse(body.SourceDocumentID); err != nil {
			driverError(ctx, 400, "invalid_document", "Source document ID is invalid")
			return
		}
		// Lock the owning profile and reject a stale or foreign source document.
		var owner string
		if err := tx.QueryRow(ctx.Request.Context(), `SELECT driver_id::TEXT FROM driver_profiles WHERE driver_id=$1::UUID FOR UPDATE`, driver).Scan(&owner); err != nil {
			reviewFailure(ctx)
			return
		}
		var latest string
		var metadata []byte
		err := tx.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,metadata FROM driver_documents WHERE driver_id=$1::UUID AND type=$2 ORDER BY submitted_at DESC,id DESC LIMIT 1 FOR UPDATE`, driver, body.Type).Scan(&latest, &metadata)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && latest != body.SourceDocumentID) {
			driverError(ctx, 409, "document_changed", "The document changed. Reload before saving.")
			return
		}
		if err != nil {
			reviewFailure(ctx)
			return
		}
		var source struct {
			Files []documentEvidenceFile `json:"files"`
		}
		if json.Unmarshal(metadata, &source) != nil {
			reviewFailure(ctx)
			return
		}
		files = source.Files
	}
	for _, item := range uploads {
		files = append(files, documentEvidenceFile{Side: item.side, StorageKey: item.key, ContentType: item.contentType, SizeBytes: item.size})
	}
	files, err = completeDocumentEvidence(body.Type, files)
	if err != nil {
		driverError(ctx, 400, "invalid_document_sides", err.Error())
		return
	}
	metadata, err := json.Marshal(gin.H{"files": files, "fields": body.Fields})
	if err != nil {
		reviewFailure(ctx)
		return
	}
	for _, item := range uploads {
		tag, err := tx.Exec(ctx.Request.Context(), `UPDATE admin_document_uploads SET consumed_at=NOW() WHERE id=$1 AND admin_id=$2::UUID AND consumed_at IS NULL AND expires_at>NOW()`, item.id, currentAdmin(ctx).ID)
		if err != nil || tag.RowsAffected() != 1 {
			driverError(ctx, http.StatusConflict, "upload_already_used", "Document upload is unavailable")
			return
		}
	}
	documentID := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata,expires_at) VALUES($1,$2::UUID,$3,'under_review',$4,$5::JSONB,$6)`, documentID, driver, body.Type, files[0].StorageKey, metadata, body.ExpiresAt)
	if err == nil {
		err = invalidateAdminApproval(ctx, tx, driver)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "document.submitted_by_admin", driver, gin.H{"documentId": documentID.String(), "sourceDocumentId": body.SourceDocumentID, "type": body.Type, "fieldNames": mapsKeys(body.Fields)})
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driver, "driver.document.updated", gin.H{"id": documentID.String(), "type": body.Type, "status": "under_review"})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": documentID.String(), "status": "under_review"}})
}

func mapsKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// Files are sourced only from owned reservations or the driver's current document.
type documentEvidenceFile struct {
	Side        string `json:"side"`
	StorageKey  string `json:"storageKey"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

func completeDocumentEvidence(kind string, files []documentEvidenceFile) ([]documentEvidenceFile, error) {
	bySide := map[string]documentEvidenceFile{}
	for _, file := range files {
		if !validDocumentSide(kind, file.Side) || file.StorageKey == "" {
			return nil, errors.New("Document evidence is invalid")
		}
		bySide[file.Side] = file
	}
	sides := []string{"document"}
	if kind == "driver_license" {
		sides = []string{"front", "back"}
	}
	result := make([]documentEvidenceFile, 0, len(sides))
	for _, side := range sides {
		file, ok := bySide[side]
		if !ok {
			return nil, errors.New("Required document sides are missing")
		}
		result = append(result, file)
	}
	return result, nil
}
