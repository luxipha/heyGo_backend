package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type identityProfile struct {
	FirstName       string `json:"firstName"`
	LastName        string `json:"lastName"`
	Age             *int   `json:"age"`
	NINLast4        string `json:"ninLast4"`
	BVNLast4        string `json:"bvnLast4"`
	IDType          string `json:"idType"`
	IDNumberLast4   string `json:"idNumberLast4"`
	NextOfKin       string `json:"nextOfKin"`
	Relationship    string `json:"relationship"`
	EmergencyNumber string `json:"emergencyNumber"`
}

func readIdentityProfile(ctx *gin.Context, pool *pgxpool.Pool, driver string) (identityProfile, error) {
	var profile identityProfile
	err := pool.QueryRow(ctx.Request.Context(), `SELECT first_name,last_name,age,nin_last4,bvn_last4,id_type,id_number_last4,next_of_kin,relationship,emergency_number
		FROM driver_identity_profiles WHERE driver_id=$1::UUID`, driver).Scan(&profile.FirstName, &profile.LastName, &profile.Age, &profile.NINLast4, &profile.BVNLast4, &profile.IDType, &profile.IDNumberLast4, &profile.NextOfKin, &profile.Relationship, &profile.EmergencyNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityProfile{}, nil
	}
	return profile, err
}

func normalizeIdentityProfile(body *identityProfile) bool {
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
	_, err := tx.Exec(ctx.Request.Context(), `INSERT INTO driver_identity_profiles(driver_id,first_name,last_name,age,nin_last4,bvn_last4,id_type,id_number_last4,next_of_kin,relationship,emergency_number,updated_at)
		VALUES($1::UUID,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW())
		ON CONFLICT(driver_id) DO UPDATE SET first_name=EXCLUDED.first_name,last_name=EXCLUDED.last_name,age=EXCLUDED.age,nin_last4=EXCLUDED.nin_last4,bvn_last4=EXCLUDED.bvn_last4,id_type=EXCLUDED.id_type,id_number_last4=EXCLUDED.id_number_last4,next_of_kin=EXCLUDED.next_of_kin,relationship=EXCLUDED.relationship,emergency_number=EXCLUDED.emergency_number,updated_at=NOW()`,
		driver, body.FirstName, body.LastName, body.Age, body.NINLast4, body.BVNLast4, body.IDType, body.IDNumberLast4, body.NextOfKin, body.Relationship, body.EmergencyNumber)
	return err
}

func (a *driverAPI) identity(ctx *gin.Context) {
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
	var body identityProfile
	if ctx.ShouldBindJSON(&body) != nil || !normalizeIdentityProfile(&body) {
		driverError(ctx, http.StatusBadRequest, "invalid_identity", "Identity details are invalid")
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
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: body})
}
