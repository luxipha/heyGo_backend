package handler

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type identityProfile struct {
	ResidentialAddress string            `json:"residentialAddress"`
	DateOfBirth        string            `json:"dateOfBirth"`
	EvidenceUploadIDs  map[string]string `json:"evidenceUploadIds,omitempty"`
	FirstName          string            `json:"firstName"`
	LastName           string            `json:"lastName"`
	Age                *int              `json:"age"`
	NINLast4           string            `json:"ninLast4"`
	BVNLast4           string            `json:"bvnLast4"`
	IDType             string            `json:"idType"`
	IDNumberLast4      string            `json:"idNumberLast4"`
	NextOfKin          string            `json:"nextOfKin"`
	Relationship       string            `json:"relationship"`
	EmergencyNumber    string            `json:"emergencyNumber"`
}

func readIdentityProfile(ctx *gin.Context, pool *pgxpool.Pool, driver string) (identityProfile, error) {
	var profile identityProfile
	err := pool.QueryRow(ctx.Request.Context(), `SELECT first_name,last_name,CASE WHEN date_of_birth IS NOT NULL THEN EXTRACT(YEAR FROM AGE(CURRENT_DATE,date_of_birth))::INT ELSE age END,COALESCE(TO_CHAR(date_of_birth,'YYYY-MM-DD'),''),nin_last4,bvn_last4,id_type,id_number_last4,next_of_kin,relationship,emergency_number,residential_address
		FROM driver_identity_profiles WHERE driver_id=$1::UUID`, driver).Scan(&profile.FirstName, &profile.LastName, &profile.Age, &profile.DateOfBirth, &profile.NINLast4, &profile.BVNLast4, &profile.IDType, &profile.IDNumberLast4, &profile.NextOfKin, &profile.Relationship, &profile.EmergencyNumber, &profile.ResidentialAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityProfile{}, nil
	}
	return profile, err
}

func normalizeIdentityProfile(body *identityProfile) bool {
	body.ResidentialAddress = strings.TrimSpace(body.ResidentialAddress)
	if len(body.ResidentialAddress) > 1000 {
		return false
	}
	body.DateOfBirth = strings.TrimSpace(body.DateOfBirth)
	if body.DateOfBirth != "" {
		birth, err := time.Parse("2006-01-02", body.DateOfBirth)
		if err != nil {
			return false
		}
		today := time.Now().UTC()
		age := today.Year() - birth.Year()
		if today.Month() < birth.Month() || (today.Month() == birth.Month() && today.Day() < birth.Day()) {
			age--
		}
		if age < 18 || age > 120 {
			return false
		}
		body.Age = &age
	}
	body.FirstName = strings.TrimSpace(body.FirstName)
	body.LastName = strings.TrimSpace(body.LastName)
	body.NINLast4 = strings.TrimSpace(body.NINLast4)
	body.BVNLast4 = strings.TrimSpace(body.BVNLast4)
	body.IDType = strings.TrimSpace(body.IDType)
	body.IDNumberLast4 = strings.TrimSpace(body.IDNumberLast4)
	body.NextOfKin = strings.TrimSpace(body.NextOfKin)
	body.Relationship = strings.TrimSpace(body.Relationship)
	body.EmergencyNumber = strings.TrimSpace(body.EmergencyNumber)
	if len(body.FirstName) > 100 || len(body.LastName) > 100 || len(body.IDType) > 80 || len(body.NextOfKin) > 160 || len(body.Relationship) > 80 || len(body.EmergencyNumber) > 32 || (body.Age != nil && (*body.Age < 18 || *body.Age > 120)) {
		return false
	}
	return validLast4(body.NINLast4) && validLast4(body.BVNLast4) && validLast4(body.IDNumberLast4)
}

func validLast4(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 4 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func upsertIdentityProfile(ctx *gin.Context, tx pgx.Tx, driver string, body identityProfile) error {
	_, err := tx.Exec(ctx.Request.Context(), `INSERT INTO driver_identity_profiles(driver_id,first_name,last_name,age,nin_last4,bvn_last4,id_type,id_number_last4,next_of_kin,relationship,emergency_number,date_of_birth,residential_address,updated_at)
		VALUES($1::UUID,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,'')::DATE,$13,NOW())
		ON CONFLICT(driver_id) DO UPDATE SET first_name=EXCLUDED.first_name,last_name=EXCLUDED.last_name,age=EXCLUDED.age,nin_last4=EXCLUDED.nin_last4,bvn_last4=EXCLUDED.bvn_last4,id_type=EXCLUDED.id_type,id_number_last4=EXCLUDED.id_number_last4,next_of_kin=EXCLUDED.next_of_kin,relationship=EXCLUDED.relationship,emergency_number=EXCLUDED.emergency_number,date_of_birth=EXCLUDED.date_of_birth,residential_address=EXCLUDED.residential_address,updated_at=NOW()`,
		driver, body.FirstName, body.LastName, body.Age, body.NINLast4, body.BVNLast4, body.IDType, body.IDNumberLast4, body.NextOfKin, body.Relationship, body.EmergencyNumber, body.DateOfBirth, body.ResidentialAddress)
	return err
}

func (a *driverAPI) identity(ctx *gin.Context) {
	var status string
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT manual_verification_status FROM driver_profiles WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&status); err == nil && status == "approved" {
		driverError(ctx, 403, "pin_required", "Use PIN-protected viewing for approved identity")
		return
	}

	if !a.ensureProfile(ctx) {
		return
	}
	profile, err := readIdentityProfile(ctx, a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "identity_unavailable", "Identity details are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: profile})
}

func (a *driverAPI) updateIdentity(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 16384)
	var status string
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT manual_verification_status FROM driver_profiles WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&status); err == nil && status == "approved" {
		driverError(ctx, 409, "document_locked", "Approved identity is read-only")
		return
	}

	var body identityProfile
	if ctx.ShouldBindJSON(&body) != nil || !normalizeIdentityProfile(&body) {
		driverError(ctx, http.StatusBadRequest, "invalid_identity", "Identity details are invalid")
		return
	}
	if body.ResidentialAddress == "" || body.FirstName == "" || body.LastName == "" || body.NINLast4 == "" || body.DateOfBirth == "" || len(body.EvidenceUploadIDs) != 3 {
		driverError(ctx, 422, "identity_evidence_required", "Enter your name, date of birth, residential address, NIN details and capture all three identity photos")
		return
	}
	body.IDType = "NIN"
	body.IDNumberLast4 = body.NINLast4
	if a.files == nil {
		driverError(ctx, 503, "storage_unavailable", "Identity photo storage is unavailable")
		return
	}
	if !a.ensureProfile(ctx) {
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "identity_unavailable", "Identity details could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var current string
	var submitted bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT p.manual_verification_status,EXISTS(SELECT 1 FROM driver_identity_profiles ip WHERE ip.driver_id=p.driver_id AND ip.first_name<>'' AND ip.last_name<>'' AND ip.nin_last4<>'') FROM driver_profiles p WHERE p.driver_id=$1::UUID FOR UPDATE`, driverID(ctx)).Scan(&current, &submitted)
	if err != nil {
		driverError(ctx, 503, "identity_unavailable", "Identity details could not be checked")
		return
	}
	if current == "approved" || (current == "pending" && submitted) {
		driverError(ctx, 409, "document_locked", "Identity is locked while approved or under review")
		return
	}
	batch := uuid.NewString()
	for _, kind := range []string{"driver_picture", "selfie_with_id", "id_document"} {
		upload, parseErr := uuid.Parse(body.EvidenceUploadIDs[kind])
		if parseErr != nil {
			driverError(ctx, 422, "identity_evidence_required", "Capture all three identity photos")
			return
		}
		var key, mime, side string
		var size int64
		var expires time.Time
		var consumed *time.Time
		err = tx.QueryRow(ctx.Request.Context(), `SELECT storage_key,content_type,size_bytes,side,expires_at,consumed_at FROM driver_document_uploads WHERE id=$1 AND driver_id=$2::UUID AND type=$3 FOR UPDATE`, upload, driverID(ctx), kind).Scan(&key, &mime, &size, &side, &expires, &consumed)
		if err == pgx.ErrNoRows {
			driverError(ctx, 422, "invalid_identity_evidence", "Identity photo is missing or belongs to another account")
			return
		}
		if err != nil {
			driverError(ctx, 503, "identity_unavailable", "Identity photos could not be checked")
			return
		}
		if consumed != nil || !expires.After(time.Now()) || side != "document" || (mime != "image/jpeg" && mime != "image/png") {
			driverError(ctx, 422, "invalid_identity_evidence", "Retake or upload the identity photos again")
			return
		}
		info, headErr := a.files.Head(ctx.Request.Context(), key)
		if headErr != nil || info.Size != size || !strings.EqualFold(info.ContentType, mime) {
			driverError(ctx, 422, "upload_incomplete", "Identity photo upload is incomplete")
			return
		}
		metadata, _ := json.Marshal(gin.H{"identitySubmissionId": batch, "files": []gin.H{{"side": "document", "storageKey": key, "contentType": mime, "sizeBytes": size}}})
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata) VALUES($1,$2::UUID,$3,'under_review',$4,$5::JSONB)`, uuid.New(), driverID(ctx), kind, key, metadata)
		if err != nil {
			driverError(ctx, 503, "identity_unavailable", "Identity photo could not be saved")
			return
		}
		if _, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_document_uploads SET consumed_at=NOW() WHERE id=$1`, upload); err != nil {
			driverError(ctx, 503, "identity_unavailable", "Identity upload could not be saved")
			return
		}
	}
	if err = upsertIdentityProfile(ctx, tx, driverID(ctx), body); err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET manual_verification_status='pending',manual_verification_reason='',manual_verified_by=NULL,manual_verified_at=NULL,admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID(ctx))
	}
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driverID(ctx))
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driverID(ctx), "driver.identity.updated", gin.H{"status": "under_review"})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "identity_unavailable", "Identity details could not be saved")
		return
	}
	body.EvidenceUploadIDs = nil
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: body})
}
