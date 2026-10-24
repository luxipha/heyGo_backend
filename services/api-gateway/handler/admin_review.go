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
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

func registerAdminReviewRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/drivers", a.listDrivers)
	group.GET("/drivers/summary", a.partnerSummary)
	group.GET("/drivers/:driverID", a.driverReview)
	group.GET("/drivers/:driverID/verification-timeline", a.partnerTimeline)
	group.GET("/drivers/:driverID/verified-legal-name", a.verifiedLegalName)
	group.POST("/drivers/:driverID/verified-legal-name", a.requireCSRF, a.recordVerifiedLegalName)
	group.PATCH("/drivers/:driverID/profile", a.requireCSRF, a.updateDriverProfile)
	group.PUT("/drivers/:driverID/identity", a.requireCSRF, a.updateDriverIdentity)
	group.PUT("/drivers/:driverID/vehicle", a.requireCSRF, a.updateDriverVehicle)
	group.POST("/drivers/:driverID/manual-verification", a.requireCSRF, a.reviewManualIdentity)
	group.GET("/drivers/:driverID/documents/:documentID/files/:side", a.documentFile)
	group.POST("/drivers/:driverID/documents/uploads", a.requireCSRF, a.createDocumentUpload)
	group.POST("/drivers/:driverID/documents", a.requireCSRF, a.submitDocument)
	group.POST("/drivers/:driverID/documents/:documentID/review", a.requireCSRF, a.reviewDocument)
	group.GET("/drivers/:driverID/vehicle/photos", a.vehiclePhotos)
	group.POST("/drivers/:driverID/vehicle/photos/uploads", a.requireCSRF, a.reserveVehiclePhoto)
	group.POST("/drivers/:driverID/vehicle/photos", a.requireCSRF, a.submitVehiclePhoto)
	group.POST("/drivers/:driverID/inspection/uploads", a.requireCSRF, a.createReportUpload)
	group.POST("/drivers/:driverID/inspection", a.requireCSRF, a.submitReport)
	group.GET("/drivers/:driverID/inspection/:inspectionID/report", a.inspectionReport)
	group.POST("/drivers/:driverID/inspection/:inspectionID/review", a.requireCSRF, a.reviewInspection)
	group.POST("/drivers/:driverID/approval", a.requireCSRF, a.reviewDriver)
}

func reviewDriverID(ctx *gin.Context) (string, bool) {
	id, err := uuid.Parse(ctx.Param("driverID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid driver ID is required")
		return "", false
	}
	return id.String(), true
}
func reviewID(ctx *gin.Context, param string) (string, bool) {
	id, err := uuid.Parse(ctx.Param(param))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_review_id", "Valid review ID is required")
		return "", false
	}
	return id.String(), true
}
func adminAudit(ctx *gin.Context, tx pgx.Tx, action, driverID string, details any) error {
	targetType, targetID := adminAuditTarget(action, driverID, details)
	before, after := auditStates(details)
	return insertAdminAudit(ctx, tx, currentAdmin(ctx).ID, action, driverID, targetType, targetID, details, before, after)
}
func insertDriverEvent(ctx *gin.Context, tx pgx.Tx, driverID, kind string, data any) (contracts.WSMessage, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return contracts.WSMessage{}, err
	}
	return messaging.AppendEventTx(ctx.Request.Context(), tx, driverID, uuid.NewString(), kind, raw)
}
func reviewFailure(ctx *gin.Context) {
	driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Review could not be saved")
}
func (a *adminAPI) listDrivers(ctx *gin.Context) {
	a.listPartners(ctx)
}

func (a *adminAPI) driverReview(ctx *gin.Context) {
	id, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var name, phone, email, adminStatus, adminReason, manualStatus, manualReason, applicationNumber string
	var manualVerifiedAt *time.Time
	var vehicle json.RawMessage
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT COALESCE(NULLIF((SELECT BTRIM(CONCAT_WS(' ',ip.first_name,ip.last_name)) FROM driver_identity_profiles ip WHERE ip.driver_id=p.driver_id),''),NULLIF(BTRIM(p.display_name),''),''),COALESCE(u.phone_e164,''),COALESCE(u.email,''),p.application_number,p.admin_status,p.admin_reason,p.manual_verification_status,p.manual_verification_reason,p.manual_verified_at,COALESCE((SELECT jsonb_build_object('packageSlug',v.package_slug,'make',v.make,'model',v.model,'modelYear',v.model_year,'color',v.color,'plate',v.plate,'reviewStatus',v.review_status,'catalogueModelId',v.catalogue_model_id,'passengerSeats',v.passenger_seats,'workingAc',v.working_ac,'conditionNotes',v.condition_notes,'findings',v.findings,'vehicleRevision',v.inspection_revision,'categoryIds',COALESCE((SELECT jsonb_agg(category_id) FROM driver_vehicle_categories WHERE driver_id=v.driver_id),'[]'::JSONB),'categoryNames',COALESCE((SELECT jsonb_agg(c.name ORDER BY c.name) FROM driver_vehicle_categories a JOIN ride_categories c ON c.id=a.category_id WHERE a.driver_id=v.driver_id),'[]'::JSONB)) FROM driver_vehicles v WHERE v.driver_id=p.driver_id),'{}'::JSONB) FROM driver_profiles p JOIN users u ON u.id=p.driver_id WHERE p.driver_id=$1::UUID`, id).Scan(&name, &phone, &email, &applicationNumber, &adminStatus, &adminReason, &manualStatus, &manualReason, &manualVerifiedAt, &vehicle)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	eligibility, err := driverstate.Read(ctx.Request.Context(), a.pool, id)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT DISTINCT ON(type) id::TEXT,type,status,rejection_reason,submitted_at,expires_at,metadata FROM driver_documents WHERE driver_id=$1::UUID ORDER BY type,submitted_at DESC,id DESC`, id)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer rows.Close()
	documents := []gin.H{}
	for rows.Next() {
		var docID, kind, status, reason string
		var submitted time.Time
		var expiry *time.Time
		var metadata json.RawMessage
		if rows.Scan(&docID, &kind, &status, &reason, &submitted, &expiry, &metadata) != nil {
			reviewFailure(ctx)
			return
		}
		documents = append(documents, gin.H{"id": docID, "type": kind, "status": status, "reason": reason, "submittedAt": submitted, "expiresAt": expiry, "metadata": metadata})
	}
	if rows.Err() != nil {
		reviewFailure(ctx)
		return
	}
	var inspection gin.H = gin.H{"status": "required"}
	var inspectionID, inspectionStatus, partner, location, reference, reason string
	var inspected *time.Time
	var vehicleRevision int64
	var vehicleSnapshot json.RawMessage
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,status,partner_name,location_name,partner_reference,outcome_reason,inspected_at,vehicle_revision,vehicle_snapshot FROM driver_inspections WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&inspectionID, &inspectionStatus, &partner, &location, &reference, &reason, &inspected, &vehicleRevision, &vehicleSnapshot)
	if err == nil {
		inspection = gin.H{"vehicleRevision": vehicleRevision, "vehicleSnapshot": vehicleSnapshot, "id": inspectionID, "status": inspectionStatus, "partnerName": partner, "locationName": location, "partnerReference": reference, "reason": reason, "inspectedAt": inspected}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		reviewFailure(ctx)
		return
	}
	identity, err := readIdentityProfile(ctx, a.pool, id)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	data := gin.H{"driverId": id, "displayName": name, "applicationNumber": applicationNumber, "adminStatus": adminStatus, "adminReason": adminReason, "manualVerification": gin.H{"status": manualStatus, "reason": manualReason, "reviewedAt": manualVerifiedAt}, "identity": identity, "vehicle": vehicle, "eligibility": eligibility, "documents": documents, "inspection": inspection}
	context, err := a.partnerContext(ctx, id)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	data["links"] = context
	if adminHasPermission(ctx, permissionDriverContact) {
		data["phone"], data["email"] = phone, email
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: data})
}

func (a *adminAPI) updateDriverProfile(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_profile", "Display name is required")
		return
	}
	body.DisplayName = strings.TrimSpace(body.DisplayName)
	if body.DisplayName == "" || len(body.DisplayName) > 120 {
		driverError(ctx, http.StatusBadRequest, "invalid_profile", "Display name is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET display_name=$2,admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driver, body.DisplayName)
	if err != nil || tag.RowsAffected() != 1 {
		reviewFailure(ctx)
		return
	}
	if err = invalidateAdminApproval(ctx, tx, driver); err == nil {
		err = adminAudit(ctx, tx, "driver.profile.updated_by_admin", driver, gin.H{"displayName": body.DisplayName})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": driver, "displayName": body.DisplayName, "adminStatus": "pending"}})
}

func (a *adminAPI) updateDriverVehicle(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		CatalogueModelID string          `json:"catalogueModelId"`
		CategoryIDs      []string        `json:"categoryIds"`
		PassengerSeats   int             `json:"passengerSeats"`
		WorkingAC        *bool           `json:"workingAc"`
		Findings         VehicleFindings `json:"findings"`
		ConditionNotes   string          `json:"conditionNotes"`
		PackageSlug      string          `json:"packageSlug"`
		Make             string          `json:"make"`
		Model            string          `json:"model"`
		Year             int             `json:"year"`
		Color            string          `json:"color"`
		Plate            string          `json:"plate"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.CatalogueModelID == "" && (!validPackage(body.PackageSlug) || strings.TrimSpace(body.Make) == "" || strings.TrimSpace(body.Model) == "")) || strings.TrimSpace(body.Plate) == "" || body.Year < 1900 || body.Year > time.Now().Year()+1 || len(body.Plate) > 30 {
		driverError(ctx, http.StatusBadRequest, "invalid_vehicle", "Valid package, make, model, year, and plate are required")
		return
	}
	if err := body.Findings.validate(); err != nil {
		driverError(ctx, 400, "invalid_vehicle_findings", err.Error())
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var profileID string
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT driver_id::TEXT FROM driver_profiles WHERE driver_id=$1::UUID FOR UPDATE`, driver).Scan(&profileID); err != nil {
		driverError(ctx, 404, "driver_not_found", "Driver was not found")
		return
	}

	var exists bool
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driver).Scan(&exists); err != nil || !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	if body.CatalogueModelID != "" {
		// Lock the catalogue against concurrent eligibility edits while recording evidence.
		if _, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('vehicle-catalogue'))`); err != nil {
			reviewFailure(ctx)
			return
		}
		if _, e := uuid.Parse(body.CatalogueModelID); e != nil || body.PassengerSeats < 1 || body.PassengerSeats > 100 || len(body.CategoryIDs) < 1 || len(body.CategoryIDs) > 100 || len(body.ConditionNotes) > 2000 || strings.TrimSpace(body.ConditionNotes) == "" {
			driverError(ctx, 400, "invalid_inspected_vehicle", "Select a model, categories, actual seats and condition notes")
			return
		}
		err = tx.QueryRow(ctx.Request.Context(), `SELECT make,model FROM vehicle_catalogue WHERE id=$1::UUID AND status='active' AND $2 BETWEEN year_from AND year_to`, body.CatalogueModelID, body.Year).Scan(&body.Make, &body.Model)
		if err != nil {
			driverError(ctx, 409, "model_unavailable", "Choose an active catalogue model and matching year")
			return
		}
		var primary string
		for _, categoryID := range body.CategoryIDs {
			if _, e := uuid.Parse(categoryID); e != nil {
				driverError(ctx, 400, "invalid_category", "Valid category IDs required")
				return
			}
			var slug string
			err = tx.QueryRow(ctx.Request.Context(), `SELECT c.slug FROM ride_categories c JOIN vehicle_category_models cm ON cm.category_id=c.id WHERE cm.model_id=$1::UUID AND c.id=$2::UUID AND c.status='active' AND c.passengers<=$3 AND c.minimum_year<=$4 AND (NOT c.requires_ac OR $5)`, body.CatalogueModelID, categoryID, body.PassengerSeats, body.Year, body.WorkingAC).Scan(&slug)
			if err != nil {
				driverError(ctx, 409, "vehicle_ineligible", "Selected category does not match this model, year, seats or working AC")
				return
			}
			if primary == "" {
				primary = slug
			}
		}
		body.PackageSlug = primary
	} else {
		// A classified car must not be downgraded to the legacy free-text path.
		var classified bool
		if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_vehicles WHERE driver_id=$1::UUID AND catalogue_model_id IS NOT NULL)`, driver).Scan(&classified); err != nil {
			reviewFailure(ctx)
			return
		}
		if classified {
			driverError(ctx, 409, "catalogue_required", "Select the catalogue vehicle and inspected categories")
			return
		}
	}
	var previousRevision int64
	var sameCar bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT inspection_revision,COALESCE(make=$2 AND model=$3 AND model_year=$4 AND plate=$5 AND color=$6 AND catalogue_model_id IS NOT DISTINCT FROM NULLIF($7,'')::uuid,FALSE) FROM driver_vehicles WHERE driver_id=$1::uuid FOR UPDATE`, driver, strings.TrimSpace(body.Make), strings.TrimSpace(body.Model), body.Year, strings.TrimSpace(body.Plate), strings.TrimSpace(body.Color), body.CatalogueModelID).Scan(&previousRevision, &sameCar)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		reviewFailure(ctx)
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_vehicles(driver_id,package_slug,make,model,model_year,color,plate,review_status,review_reason,reviewed_at,updated_at)
		VALUES($1::UUID,$2,$3,$4,$5,$6,$7,'approved','',NOW(),NOW())
		ON CONFLICT(driver_id) DO UPDATE SET package_slug=EXCLUDED.package_slug,make=EXCLUDED.make,model=EXCLUDED.model,model_year=EXCLUDED.model_year,color=EXCLUDED.color,plate=EXCLUDED.plate,review_status='approved',review_reason='',reviewed_at=NOW(),updated_at=NOW()`,
		driver, body.PackageSlug, strings.TrimSpace(body.Make), strings.TrimSpace(body.Model), body.Year, strings.TrimSpace(body.Color), strings.TrimSpace(body.Plate))
	if err == nil && body.CatalogueModelID != "" {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_vehicles SET catalogue_model_id=$2::UUID,passenger_seats=$3,working_ac=$4,condition_notes=$5,inspection_revision=inspection_revision+1 WHERE driver_id=$1::UUID`, driver, body.CatalogueModelID, body.PassengerSeats, body.WorkingAC, strings.TrimSpace(body.ConditionNotes))
		if err == nil {
			_, err = tx.Exec(ctx.Request.Context(), `DELETE FROM driver_vehicle_categories WHERE driver_id=$1::UUID`, driver)
		}
		for _, id := range body.CategoryIDs {
			if err != nil {
				break
			}
			_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_vehicle_categories(driver_id,category_id,reviewed_by) VALUES($1::UUID,$2::UUID,$3::UUID)`, driver, id, currentAdmin(ctx).ID)
		}
	}
	if err == nil {
		findings, _ := json.Marshal(body.Findings)
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_vehicles SET findings=$2::jsonb,inspection_revision=inspection_revision+CASE WHEN catalogue_model_id IS NULL THEN 1 ELSE 0 END WHERE driver_id=$1::UUID`, driver, findings)
	}
	if err == nil && sameCar {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_vehicle_photos(id,driver_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type) SELECT gen_random_uuid(),p.driver_id,v.inspection_revision,p.angle,p.storage_key,p.content_type,p.size_bytes,p.uploaded_by,p.uploader_type FROM driver_vehicle_photos p JOIN driver_vehicles v ON v.driver_id=p.driver_id WHERE p.driver_id=$1::uuid AND p.vehicle_revision=$2 AND p.superseded_at IS NULL AND v.inspection_revision<>$2`, driver, previousRevision)
	}
	if err == nil {
		err = invalidateAdminApproval(ctx, tx, driver)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "driver.vehicle.updated_by_admin", driver, body)
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driver, "driver.vehicle.updated", gin.H{"reviewStatus": "approved"})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": driver, "reviewStatus": "approved", "adminStatus": "pending"}})
}

func (a *adminAPI) reviewManualIdentity(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status != "approved" && body.Status != "rejected") || (body.Status == "rejected" && strings.TrimSpace(body.Reason) == "") || len(body.Reason) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "Valid manual verification decision and rejection reason are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET manual_verification_status=$2,manual_verification_reason=$3,manual_verified_by=$4::UUID,manual_verified_at=NOW(),admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driver, body.Status, strings.TrimSpace(body.Reason), currentAdmin(ctx).ID)
	if err != nil || tag.RowsAffected() != 1 {
		reviewFailure(ctx)
		return
	}
	// Identity evidence shares the manual NIN review decision; it is not an extra Home gate.
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_documents d SET status=$2,rejection_reason=$3,reviewed_at=NOW() WHERE d.driver_id=$1::UUID AND d.type IN ('driver_picture','selfie_with_id','id_document') AND d.id=(SELECT latest.id FROM driver_documents latest WHERE latest.driver_id=d.driver_id AND latest.type=d.type ORDER BY latest.submitted_at DESC,latest.id DESC LIMIT 1) AND d.status='under_review'`, driver, body.Status, strings.TrimSpace(body.Reason))
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = invalidateAdminApproval(ctx, tx, driver); err == nil {
		err = adminAudit(ctx, tx, "driver.manual_identity.review", driver, gin.H{"status": body.Status, "reason": strings.TrimSpace(body.Reason)})
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driver, "driver.onboarding.updated", gin.H{"manualVerificationStatus": body.Status, "reason": strings.TrimSpace(body.Reason)})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": driver, "manualVerificationStatus": body.Status}})
}
func (a *adminAPI) documentFile(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	doc, ok := reviewID(ctx, "documentID")
	if !ok {
		return
	}
	side := ctx.Param("side")
	if side != "front" && side != "back" && side != "document" {
		driverError(ctx, http.StatusBadRequest, "invalid_side", "Document side is invalid")
		return
	}
	var metadata []byte
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT metadata FROM driver_documents WHERE id=$1::UUID AND driver_id=$2::UUID`, doc, driver).Scan(&metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "document_not_found", "Document was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	var data struct {
		Files []struct {
			Side       string `json:"side"`
			StorageKey string `json:"storageKey"`
		} `json:"files"`
	}
	if json.Unmarshal(metadata, &data) != nil {
		reviewFailure(ctx)
		return
	}
	for _, f := range data.Files {
		if f.Side == side {
			url, err := a.files.PresignGet(ctx.Request.Context(), f.StorageKey, 5*time.Minute)
			if err != nil {
				reviewFailure(ctx)
				return
			}
			ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"url": url, "expiresInSeconds": 300}})
			return
		}
	}
	driverError(ctx, http.StatusNotFound, "file_not_found", "Document side was not found")
}
func (a *adminAPI) reviewDocument(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	doc, ok := reviewID(ctx, "documentID")
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status != "approved" && body.Status != "rejected") || (body.Status == "rejected" && strings.TrimSpace(body.Reason) == "") || len(body.Reason) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "Valid status and rejection reason are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var kind string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT d.type FROM driver_documents d WHERE d.id=$1::UUID AND d.driver_id=$2::UUID AND d.status='under_review' AND d.id=(SELECT d2.id FROM driver_documents d2 WHERE d2.driver_id=$2::UUID AND d2.type=d.type ORDER BY d2.submitted_at DESC,d2.id DESC LIMIT 1) FOR UPDATE`, doc, driver).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "document_not_current", "Document is not awaiting review")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_documents SET status=$1,rejection_reason=$2,reviewed_at=NOW(),reviewed_by=$3::UUID,updated_at=NOW() WHERE id=$4::UUID`, body.Status, body.Reason, currentAdmin(ctx).ID, doc)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if body.Status == "rejected" {
		if err = invalidateAdminApproval(ctx, tx, driver); err != nil {
			reviewFailure(ctx)
			return
		}
	}
	if err = adminAudit(ctx, tx, "document.review", driver, gin.H{"documentId": doc, "type": kind, "status": body.Status, "reason": body.Reason}); err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = insertDriverEvent(ctx, tx, driver, "driver.document.updated", gin.H{"id": doc, "type": kind, "status": body.Status, "reason": body.Reason})
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": doc, "status": body.Status}})
}
func invalidateAdminApproval(ctx *gin.Context, tx pgx.Tx, driver string) error {
	if _, err := tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driver); err != nil {
		return err
	}
	_, err := tx.Exec(ctx.Request.Context(), `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driver)
	return err
}
func (a *adminAPI) createReportUpload(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		ContentType string `json:"contentType"`
		SizeBytes   int64  `json:"sizeBytes"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validDocumentContentType(body.ContentType) || body.SizeBytes <= 0 || body.SizeBytes > maxDriverDocumentBytes {
		driverError(ctx, http.StatusBadRequest, "invalid_report_upload", "Report type or size is invalid")
		return
	}
	var exists bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driver).Scan(&exists); err != nil {
		reviewFailure(ctx)
		return
	}
	if !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	id := uuid.New()
	key := fmt.Sprintf("inspection-reports/%s/%s", driver, id)
	expires := time.Now().UTC().Add(uploadLifetime)
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_report_uploads(id,admin_id,driver_id,storage_key,content_type,size_bytes,expires_at) VALUES($1,$2::UUID,$3::UUID,$4,$5,$6,$7)`, id, currentAdmin(ctx).ID, driver, key, body.ContentType, body.SizeBytes, expires)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	url, headers, err := a.files.PresignPut(ctx.Request.Context(), key, body.ContentType, body.SizeBytes, uploadLifetime)
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM admin_report_uploads WHERE id=$1`, id)
		reviewFailure(ctx)
		return
	}
	_, err = a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_audit(id,admin_id,action,target_driver_id,target_type,target_id,details,before_state,after_state)
		VALUES($1,$2::UUID,'inspection.upload_reserved',$3::UUID,'inspection_upload',$4,'{}'::JSONB,'{}'::JSONB,'{}'::JSONB)`, uuid.New(), currentAdmin(ctx).ID, driver, id.String())
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM admin_report_uploads WHERE id=$1`, id)
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"uploadId": id, "url": url, "method": "PUT", "headers": headers, "expiresAt": expires}})
}
func (a *adminAPI) submitReport(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		UploadID         string     `json:"uploadId"`
		PartnerName      string     `json:"partnerName"`
		LocationName     string     `json:"locationName"`
		PartnerReference string     `json:"partnerReference"`
		InspectedAt      *time.Time `json:"inspectedAt"`
	}
	if ctx.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.PartnerName) == "" || strings.TrimSpace(body.LocationName) == "" || len(body.PartnerName) > 200 || len(body.LocationName) > 200 || len(body.PartnerReference) > 200 {
		driverError(ctx, http.StatusBadRequest, "invalid_inspection_report", "Partner and inspection location are required")
		return
	}
	uploadID, err := uuid.Parse(body.UploadID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_upload_id", "Upload ID is invalid")
		return
	}
	var key, contentType string
	var size int64
	var expiry time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT storage_key,content_type,size_bytes,expires_at FROM admin_report_uploads WHERE id=$1 AND admin_id=$2::UUID AND driver_id=$3::UUID AND consumed_at IS NULL`, uploadID, currentAdmin(ctx).ID, driver).Scan(&key, &contentType, &size, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusBadRequest, "invalid_upload", "Report upload is missing or expired")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if !expiry.After(time.Now()) {
		driverError(ctx, http.StatusBadRequest, "invalid_upload", "Report upload is missing or expired")
		return
	}
	info, err := a.files.Head(ctx.Request.Context(), key)
	if err != nil || info.Size != size || !strings.EqualFold(info.ContentType, contentType) {
		driverError(ctx, http.StatusBadRequest, "upload_incomplete", "Report file does not match its reservation")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var lockedRevision int64
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT inspection_revision FROM driver_vehicles WHERE driver_id=$1::uuid FOR UPDATE`, driver).Scan(&lockedRevision); err != nil {
		driverError(ctx, 409, "vehicle_required", "Save inspected vehicle details before submitting a report")
		return
	}
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE admin_report_uploads SET consumed_at=NOW() WHERE id=$1 AND consumed_at IS NULL AND expires_at>NOW()`, uploadID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "upload_already_used", "Report upload is unavailable")
		return
	}
	id := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_inspections(id,driver_id,partner_name,location_name,partner_reference,report_storage_key,report_received_at,status,inspected_at,vehicle_revision,vehicle_snapshot,evidence_version) VALUES($1,$2::UUID,$3,$4,$5,$6,NOW(),'report_received',$7,COALESCE((SELECT inspection_revision FROM driver_vehicles WHERE driver_id=$2::UUID),0),(SELECT to_jsonb(v) FROM driver_vehicles v WHERE driver_id=$2::UUID),1)`, id, driver, strings.TrimSpace(body.PartnerName), strings.TrimSpace(body.LocationName), strings.TrimSpace(body.PartnerReference), key, body.InspectedAt)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = invalidateAdminApproval(ctx, tx, driver); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = adminAudit(ctx, tx, "inspection.report_received", driver, gin.H{"inspectionId": id, "partnerName": body.PartnerName, "locationName": body.LocationName}); err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = insertDriverEvent(ctx, tx, driver, "driver.inspection.updated", gin.H{"id": id, "status": "report_received"})
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "status": "report_received"}})
}
func (a *adminAPI) inspectionReport(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "document_storage_unavailable", "Document storage is unavailable")
		return
	}
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	inspection, ok := reviewID(ctx, "inspectionID")
	if !ok {
		return
	}
	var key string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT report_storage_key FROM driver_inspections WHERE id=$1::UUID AND driver_id=$2::UUID`, inspection, driver).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "inspection_not_found", "Inspection was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	url, err := a.files.PresignGet(ctx.Request.Context(), key, 5*time.Minute)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"url": url, "expiresInSeconds": 300}})
}
func (a *adminAPI) reviewInspection(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	inspection, ok := reviewID(ctx, "inspectionID")
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status != "completed" && body.Status != "failed") || (body.Status == "failed" && strings.TrimSpace(body.Reason) == "") || len(body.Reason) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "Valid outcome and failure reason are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var profileID string
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT driver_id::TEXT FROM driver_profiles WHERE driver_id=$1::UUID FOR UPDATE`, driver).Scan(&profileID); err != nil {
		driverError(ctx, 404, "driver_not_found", "Driver was not found")
		return
	}

	if body.Status == "completed" {
		if err := validateInspectionEvidence(ctx, tx, driver, inspection); err != nil {
			driverError(ctx, 409, "inspection_evidence_incomplete", err.Error())
			return
		}
		var valid bool
		err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_inspections i JOIN driver_vehicles v ON v.driver_id=i.driver_id WHERE i.id=$1::UUID AND i.driver_id=$2::UUID AND i.vehicle_revision=v.inspection_revision AND (v.catalogue_model_id IS NULL OR EXISTS(SELECT 1 FROM ride_categories c WHERE driver_category_eligible(v.driver_id,c.slug))))`, inspection, driver).Scan(&valid)
		if err != nil {
			reviewFailure(ctx)
			return
		}
		if !valid {
			driverError(ctx, 409, "inspection_vehicle_changed", "Save the inspected vehicle and submit a current inspection report before approval")
			return
		}
	}
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE driver_inspections SET status=$1,outcome_reason=$2,reviewed_by=$3::UUID,updated_at=NOW() WHERE id=$4::UUID AND driver_id=$5::UUID AND status='report_received' AND report_storage_key IS NOT NULL AND id=(SELECT id FROM driver_inspections WHERE driver_id=$5::UUID ORDER BY created_at DESC,id DESC LIMIT 1)`, body.Status, body.Reason, currentAdmin(ctx).ID, inspection, driver)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "inspection_not_current", "Inspection is not awaiting review")
		return
	}
	if body.Status == "failed" {
		if err = invalidateAdminApproval(ctx, tx, driver); err != nil {
			reviewFailure(ctx)
			return
		}
	}
	if err = adminAudit(ctx, tx, "inspection.review", driver, gin.H{"inspectionId": inspection, "status": body.Status, "reason": body.Reason}); err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = insertDriverEvent(ctx, tx, driver, "driver.inspection.updated", gin.H{"id": inspection, "status": body.Status, "reason": body.Reason})
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": inspection, "status": body.Status}})
}
func (a *adminAPI) reviewDriver(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status != "approved" && body.Status != "rejected") || (body.Status == "rejected" && strings.TrimSpace(body.Reason) == "") || len(body.Reason) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "Valid decision and rejection reason are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize decisions and driver submissions through the profile row.
	var existing string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT admin_status FROM driver_profiles WHERE driver_id=$1::UUID FOR UPDATE`, driver).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if body.Status == "approved" {
		var ready bool
		err = tx.QueryRow(ctx.Request.Context(), `SELECT (u.phone_verified_at IS NOT NULL OR u.email_verified) AND p.manual_verification_status='approved' AND NOT EXISTS(SELECT 1 FROM driver_vehicles v WHERE v.driver_id=u.id AND v.catalogue_model_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM ride_categories c WHERE driver_category_eligible(v.driver_id,c.slug)))
	   AND COALESCE((SELECT status='completed' AND vehicle_revision=COALESCE((SELECT inspection_revision FROM driver_vehicles WHERE driver_id=u.id),0) FROM driver_inspections WHERE driver_id=u.id ORDER BY created_at DESC,id DESC LIMIT 1),FALSE)
   AND NOT EXISTS(SELECT 1 FROM (VALUES('driver_license'),('vehicle_license'),('roadworthiness'),('auto_insurance'),('hackney_permit')) required(type)
    WHERE NOT COALESCE((SELECT status='approved' AND (expires_at IS NULL OR expires_at>NOW()) FROM driver_documents d WHERE d.driver_id=u.id AND d.type=required.type ORDER BY submitted_at DESC,id DESC LIMIT 1),FALSE))
	   FROM users u JOIN driver_profiles p ON p.driver_id=u.id WHERE u.id=$1::UUID AND 'driver'=ANY(u.roles)`, driver).Scan(&ready)
		if err != nil {
			reviewFailure(ctx)
			return
		}
		if !ready {
			driverError(ctx, http.StatusConflict, "requirements_incomplete", "All seven requirements must be complete before approval")
			return
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET admin_status=$1,admin_reason=$2,admin_reviewed_at=NOW(),updated_at=NOW() WHERE driver_id=$3::UUID`, body.Status, body.Reason, driver)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if body.Status == "rejected" {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driver)
		if err != nil {
			reviewFailure(ctx)
			return
		}
	}
	if err = adminAudit(ctx, tx, "driver.approval", driver, gin.H{"status": body.Status, "reason": body.Reason}); err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = insertDriverEvent(ctx, tx, driver, "driver.onboarding.updated", gin.H{"adminStatus": body.Status, "reason": body.Reason})
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": driver, "adminStatus": body.Status}})
}
