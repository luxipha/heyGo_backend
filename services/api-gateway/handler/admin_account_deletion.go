package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminAccountDeletionRequest struct {
	ID                    string     `json:"id"`
	DriverID              string     `json:"driverId"`
	DriverName            string     `json:"driverName"`
	Status                string     `json:"status"`
	RequestedAt           time.Time  `json:"requestedAt"`
	ReviewedBy            *string    `json:"reviewedBy,omitempty"`
	ReviewedAt            *time.Time `json:"reviewedAt,omitempty"`
	ReviewReason          string     `json:"reviewReason,omitempty"`
	StorageCleanupPending int        `json:"storageCleanupPending"`
	CompletedAt           *time.Time `json:"completedAt,omitempty"`
}

func (a *adminAPI) listAccountDeletionRequests(ctx *gin.Context) {
	status := ctx.DefaultQuery("status", "pending")
	if status != "pending" && status != "approved" && status != "rejected" && status != "all" {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Status must be pending, approved, rejected, or all")
		return
	}
	limit, err := parseDriverTripHistoryLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursor *driverTripHistoryCursor
	if value := ctx.Query("cursor"); value != "" {
		decoded, err := decodeDriverTripHistoryCursor(value)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Request cursor is invalid")
			return
		}
		cursor = &decoded
	}
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT r.id::TEXT,r.driver_id::TEXT,COALESCE(NULLIF(p.display_name,''),'Deleted or unnamed Driver'),
		r.status,r.requested_at,r.reviewed_by::TEXT,r.reviewed_at,r.review_reason,
		(SELECT COUNT(*)::INTEGER FROM driver_account_deletion_files f WHERE f.request_id=r.id AND f.deleted_at IS NULL),r.completed_at
		FROM driver_account_deletion_requests r LEFT JOIN driver_profiles p ON p.driver_id=r.driver_id
		WHERE ($1='all' OR r.status=$1) AND ($2::TIMESTAMPTZ IS NULL OR (r.requested_at,r.id)<($2,$3::UUID))
		ORDER BY r.requested_at DESC,r.id DESC LIMIT $4`, status, cursorTime, cursorID, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "Account deletion requests are unavailable")
		return
	}
	defer rows.Close()
	items := make([]adminAccountDeletionRequest, 0, limit+1)
	for rows.Next() {
		var item adminAccountDeletionRequest
		if err := rows.Scan(&item.ID, &item.DriverID, &item.DriverName, &item.Status, &item.RequestedAt, &item.ReviewedBy, &item.ReviewedAt, &item.ReviewReason, &item.StorageCleanupPending, &item.CompletedAt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "Account deletion requests are unavailable")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "Account deletion requests are unavailable")
		return
	}
	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: last.RequestedAt, ID: last.ID})
		nextCursor = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: nextCursor}})
}

func (a *adminAPI) reviewAccountDeletionRequest(ctx *gin.Context) {
	requestID, err := uuid.Parse(ctx.Param("requestID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_request_id", "A valid request ID is required")
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "A decision of approve or reject is required")
		return
	}
	if body.Decision != "approve" && body.Decision != "reject" {
		driverError(ctx, http.StatusBadRequest, "invalid_review", "A decision of approve or reject is required")
		return
	}
	if body.Decision == "reject" && len([]rune(body.Reason)) == 0 || len([]rune(body.Reason)) > 500 {
		driverError(ctx, http.StatusBadRequest, "review_reason_required", "A rejection reason of at most 500 characters is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The request could not be reviewed")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var driver string
	var status string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT driver_id::TEXT,status FROM driver_account_deletion_requests WHERE id=$1::UUID FOR UPDATE`, requestID).Scan(&driver, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "account_deletion_not_found", "Account deletion request was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The request could not be reviewed")
		return
	}
	if status != "pending" {
		driverError(ctx, http.StatusConflict, "account_deletion_already_reviewed", "Only pending requests can be reviewed")
		return
	}
	if body.Decision == "approve" {
		var activeTrip bool
		if err := tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trips WHERE assigned_driver_id=$1::UUID AND status IN ('assigned','accepted','arrived','started'))`, driver).Scan(&activeTrip); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The request could not be reviewed")
			return
		}
		if activeTrip {
			driverError(ctx, http.StatusConflict, "driver_trip_active", "Account deletion cannot be approved while the Driver has an active trip or offer")
			return
		}
		if err := queueDriverDeletionFiles(ctx, tx, requestID.String(), driver); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "Personal files could not be queued for secure removal")
			return
		}
		if err := anonymizeDriverAccount(ctx, tx, requestID.String(), driver); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The Driver account could not be deactivated")
			return
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_account_deletion_requests SET status=$2,reviewed_by=$3::UUID,reviewed_at=NOW(),review_reason=$4,
		completed_at=CASE WHEN $2='rejected' OR NOT EXISTS(SELECT 1 FROM driver_account_deletion_files f WHERE f.request_id=$1::UUID AND f.deleted_at IS NULL) THEN NOW() ELSE NULL END
		WHERE id=$1::UUID AND status='pending'`, requestID, map[string]string{"approve": "approved", "reject": "rejected"}[body.Decision], currentAdmin(ctx).ID, body.Reason)
	if err == nil {
		err = adminAudit(ctx, tx, "driver.account_deletion."+body.Decision, driver, gin.H{"requestId": requestID.String(), "reason": body.Reason})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The request could not be reviewed")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "status": map[string]string{"approve": "approved", "reject": "rejected"}[body.Decision]}})
}

func queueDriverDeletionFiles(ctx *gin.Context, tx pgx.Tx, requestID, driverID string) error {
	_, err := tx.Exec(ctx.Request.Context(), `INSERT INTO driver_account_deletion_files(request_id,storage_key)
		SELECT $1::UUID,storage_key FROM (
			SELECT storage_key FROM driver_documents WHERE driver_id=$2::UUID
			UNION SELECT storage_key FROM driver_document_uploads WHERE driver_id=$2::UUID
			UNION SELECT report_storage_key FROM driver_inspections WHERE driver_id=$2::UUID AND report_storage_key IS NOT NULL
			UNION SELECT archive_storage_key FROM driver_data_exports WHERE driver_id=$2::UUID AND status IN ('pending','processing','sent','failed') AND archive_storage_key IS NOT NULL
		) keys ON CONFLICT DO NOTHING`, requestID, driverID)
	return err
}

func anonymizeDriverAccount(ctx *gin.Context, tx pgx.Tx, requestID, driverID string) error {
	if _, err := tx.Exec(ctx.Request.Context(), `INSERT INTO deactivated_casperid_identities(subject_hash,human_id_hash,deletion_request_id)
		SELECT casper_subject_hash,human_id_hash,id FROM driver_account_deletion_requests WHERE id=$1::UUID`, requestID); err != nil {
		return err
	}
	queries := []struct {
		query string
		args  []any
	}{
		{`UPDATE users SET active=FALSE,deactivated_at=NOW(),casper_id_user_id='deleted:'||id::TEXT,human_id='deleted-'||id::TEXT,
			roles='{}'::TEXT[],verified=FALSE,nin_verified=FALSE,kyc_tier=NULL,trust_score=NULL,updated_at=NOW() WHERE id=$1::UUID AND active=TRUE`, []any{driverID}},
		{`UPDATE driver_profiles SET display_name='Deleted Driver',photo_url='',admin_reason='',updated_at=NOW() WHERE driver_id=$1::UUID`, []any{driverID}},
		{`UPDATE driver_vehicles SET package_slug='',make='',model='',model_year=NULL,color='',plate='',review_reason='',review_status='pending',updated_at=NOW() WHERE driver_id=$1::UUID`, []any{driverID}},
		{`UPDATE drivers SET name='Deleted Driver',profile_pic='',car_plate='',package_slug='',status='offline',available=FALSE,online_requested=FALSE,location=NULL,online_market_code=NULL,updated_at=NOW() WHERE id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_safety_contacts WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_devices WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_socket_sessions WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_live_locations WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_locations WHERE driver_id=$1`, []any{driverID}},
		{`DELETE FROM driver_documents WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_document_uploads WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM driver_inspections WHERE driver_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM api_idempotency_keys WHERE actor_id=$1::UUID`, []any{driverID}},
		{`DELETE FROM user_events WHERE recipient_id=$1::UUID`, []any{driverID}},
		{`UPDATE driver_data_exports SET status='cancelled',requested_email='',archive_storage_key=NULL,archive_link_ciphertext=NULL,mail_from_email=NULL,mail_from_name=NULL,lease_until=NULL,updated_at=NOW() WHERE driver_id=$1::UUID AND status IN ('pending','processing','sent','failed')`, []any{driverID}},
	}
	for _, item := range queries {
		if _, err := tx.Exec(ctx.Request.Context(), item.query, item.args...); err != nil {
			return err
		}
	}
	return nil
}
