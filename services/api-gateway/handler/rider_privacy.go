package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"io"
	"time"
)

func privacyRequestID(c *gin.Context) (string, bool) {
	var body struct {
		Confirmed bool   `json:"confirmed"`
		ID        string `json:"clientRequestId"`
	}
	if c.ShouldBindJSON(&body) != nil || !body.Confirmed {
		driverError(c, 400, "confirmation_required", "Explicit confirmation and a stable clientRequestId are required")
		return "", false
	}
	id, err := uuid.Parse(body.ID)
	if err != nil {
		driverError(c, 400, "invalid_request_id", "clientRequestId must be a UUID")
		return "", false
	}
	return id.String(), true
}
func (a *riderAccountAPI) requestExport(c *gin.Context) {
	id, ok := privacyRequestID(c)
	if !ok {
		return
	}
	var owner, scope string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT rider_id::TEXT,'rider' FROM rider_data_exports WHERE id=$1::UUID`, id).Scan(&owner, &scope)
	if err == nil {
		if owner != riderAccountOwner(c) || scope != "rider" {
			driverError(c, 409, "request_id_reused", "Request identity is already in use")
			return
		}
		a.exportStatus(c, id)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		accountError(c)
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	if !user.EmailVerified || !validResendEmail(user.Email) {
		driverError(c, 409, "verified_email_required", "A verified account email is required for private delivery")
		return
	}
	if len(a.deliveryKey) != 32 {
		driverError(c, 503, "email_delivery_unavailable", "Private delivery encryption is not configured")
		return
	}
	if a.files == nil {
		driverError(c, 503, "private_storage_unavailable", "Private archive storage is not configured")
		return
	}
	var configured bool
	if a.pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM admin_resend_settings WHERE id=1)`).Scan(&configured) != nil || !configured {
		driverError(c, 503, "email_delivery_unavailable", "Private archive delivery is not configured")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	// Recheck the verified recipient after the account lock; account corrections
	// cannot cause an archive to be queued to a stale or unverified address.
	var recipient string
	if err = tx.QueryRow(c.Request.Context(), `SELECT email FROM users WHERE id=$1::UUID AND email_verified=TRUE`, user.ID).Scan(&recipient); err != nil || recipient != user.Email {
		driverError(c, 409, "verified_email_changed", "Reload your verified account email before requesting data")
		return
	}
	var existingID string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM rider_data_exports WHERE rider_id=$1::UUID AND status IN ('pending','processing')`, user.ID).Scan(&existingID)
	if err == nil {
		if existingID != id {
			driverError(c, 409, "data_export_pending", "An export request is already pending; read its status")
			return
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_data_exports(id,rider_id,requested_email) VALUES($1::UUID,$2::UUID,$3)`, id, user.ID, recipient)
	} else {
		accountError(c)
		return
	}
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.Status(202)
	a.exportStatus(c, id)
}
func (a *riderAccountAPI) exportStatus(c *gin.Context, id string) {
	var state string
	var requested time.Time
	var sent, expires *time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT status,requested_at,sent_at,expires_at FROM rider_data_exports WHERE id=$1::UUID AND rider_id=$2::UUID`, id, riderAccountOwner(c)).Scan(&state, &requested, &sent, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "data_export_not_found", "Export was not found")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	status := 200
	if c.Writer.Status() == 202 {
		status = 202
	}
	c.JSON(status, contracts.APIResponse{Data: gin.H{"id": id, "status": state, "requestedAt": requested, "sentAt": sent, "expiresAt": expires, "delivery": "verified_email", "downloadLinkAvailableInAPI": false}})
}
func (a *riderAccountAPI) getExport(c *gin.Context) {
	id, err := uuid.Parse(c.Param("exportID"))
	if err != nil {
		driverError(c, 400, "invalid_export_id", "Valid export ID is required")
		return
	}
	a.exportStatus(c, id.String())
}
func (a *riderAccountAPI) latestExport(c *gin.Context) {
	var id string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM rider_data_exports WHERE rider_id=$1::UUID ORDER BY requested_at DESC,id DESC LIMIT 1`, riderAccountOwner(c)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, contracts.APIResponse{Data: gin.H{"request": nil}})
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	a.exportStatus(c, id)
}
func (a *riderAccountAPI) requestDeletion(c *gin.Context) {
	id, ok := privacyRequestID(c)
	if !ok {
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var existingOwner, state string
	err = tx.QueryRow(c.Request.Context(), `SELECT driver_id::TEXT,status FROM driver_account_deletion_requests WHERE id=$1::UUID`, id).Scan(&existingOwner, &state)
	if err == nil {
		if existingOwner != riderAccountOwner(c) {
			driverError(c, 409, "request_id_reused", "Request identity is already in use")
			return
		}
		c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "status": state, "scope": "account_wide"}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		accountError(c)
		return
	}
	var pending string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM driver_account_deletion_requests WHERE driver_id=$1::UUID AND status='pending'`, riderAccountOwner(c)).Scan(&pending)
	if err == nil {
		if pending != id {
			driverError(c, 409, "account_deletion_pending", "An account deletion request is already pending; read its status")
			return
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		user, _ := gatewayauth.CurrentUser(c)
		phone, email := "", ""
		if user.PhoneVerified {
			phone = user.Phone
		}
		if user.EmailVerified {
			email = user.Email
		}
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_account_deletion_requests(id,driver_id,casper_subject_hash,human_id_hash,phone_hash,email_hash,requester_role) VALUES($1::UUID,$2::UUID,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),'rider')`, id, user.ID, hashIdentityForPrivacy(user.CasperSubject), hashIdentityForPrivacy(user.HumanID), hashIdentityForPrivacy(phone), hashIdentityForPrivacy(email))
	} else {
		accountError(c)
		return
	}
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(202, contracts.APIResponse{Data: gin.H{"id": id, "status": "pending", "scope": "account_wide", "policy": "staff_review_retained_trip_financial_records"}})
}
func (a *riderAccountAPI) getDeletion(c *gin.Context) {
	var data []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT jsonb_build_object('id',id,'status',status,'requestedAt',requested_at,'reviewedAt',reviewed_at,'completedAt',completed_at,'scope','account_wide','storageCleanupPending',(SELECT COUNT(*) FROM driver_account_deletion_files f WHERE f.request_id=r.id AND deleted_at IS NULL)) FROM driver_account_deletion_requests r WHERE driver_id=$1::UUID ORDER BY requested_at DESC,id DESC LIMIT 1),'null'::JSONB)`, riderAccountOwner(c)).Scan(&data)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"request": json.RawMessage(data)}})
}

// This manifest allowlists Rider data. It excludes provider secrets, sessions,
// identity-linking fields, staff notes, Driver finances and raw event telemetry.
func buildRiderDataArchive(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, owner, exportID string) (string, error) {
	var data []byte
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'schemaVersion',1,'currency','NGN','generatedAt',NOW(),
 'identity',(SELECT jsonb_build_object('id',id,'phone',phone_e164,'email',email,'emailVerified',email_verified,'name',COALESCE((SELECT display_name FROM rider_profiles WHERE rider_id=u.id),'')) FROM users u WHERE id=$1::UUID),
 'savedPlaces',COALESCE((SELECT jsonb_agg(jsonb_build_object('label',label,'provider',provider,'placeId',place_id,'verifiedAt',verified_at)) FROM rider_saved_places WHERE rider_id=$1::UUID),'[]'::JSONB),
 'trustedContacts',COALESCE((SELECT jsonb_agg(jsonb_build_object('fullName',full_name,'relationship',relationship,'phoneNumber',phone_e164)) FROM rider_trusted_contacts WHERE rider_id=$1::UUID),'[]'::JSONB),
 'preferences',(SELECT jsonb_build_object('reduceMotion',reduce_motion,'shareRideStatus',share_ride_status) FROM rider_preferences WHERE rider_id=$1::UUID),
 'notificationPreferences',(SELECT jsonb_build_object('rideUpdates',ride_updates,'accountAlerts',account_alerts,'promotions',promotions) FROM rider_notification_preferences WHERE rider_id=$1::UUID),
 'notifications',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id::TEXT,'type',type,'title',title,'message',message,'target',data,'createdAt',created_at,'readAt',read_at) ORDER BY id) FROM rider_notifications WHERE rider_id=$1::UUID),'[]'::JSONB),
 'consumerTrustSnapshots',COALESCE((SELECT jsonb_agg(jsonb_build_object('score',score,'status',status,'calculatedAt',calculated_at,'policyVersion',policy_version,'recordedAt',recorded_at) ORDER BY id) FROM consumer_trust_snapshots WHERE user_id=$1::UUID),'[]'::JSONB),
 'trips',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',t.id,'status',t.status,'createdAt',t.created_at,'completedAt',t.completed_at,'cancelledAt',t.cancelled_at,'pickup',f.pickup_metadata,'destination',f.destination_metadata,'fareKobo',ROUND(f.total_fare_minor)::BIGINT,'noShowDebtKobo',t.no_show_debt_kobo,'amountDueKobo',ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,'settlementStatus',s.status,'providerVerified',FALSE) ORDER BY t.created_at) FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id LEFT JOIN trip_settlements s ON s.trip_id=t.id WHERE t.rider_id=$1::UUID),'[]'::JSONB),
 'tripChat',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',m.id,'tripId',m.trip_id,'body',m.body,'sentByRider',m.sender_id=$1::UUID,'createdAt',m.created_at,'readAt',m.read_at) ORDER BY m.created_at) FROM trip_chat_messages m JOIN trips t ON t.id=m.trip_id WHERE t.rider_id=$1::UUID AND (m.sender_id=$1::UUID OR m.recipient_id=$1::UUID)),'[]'::JSONB),
 'submittedRatings',COALESCE((SELECT jsonb_agg(jsonb_build_object('tripId',r.trip_id,'rating',r.rating,'comment',r.comment,'createdAt',r.created_at)) FROM trip_ratings r JOIN trips t ON t.id=r.trip_id WHERE r.actor_id=$1::UUID AND t.rider_id=$1::UUID),'[]'::JSONB),
 'receivedFeedback',COALESCE((SELECT jsonb_agg(jsonb_build_object('tripId',r.trip_id,'rating',r.rating,'tags',r.feedback_tags,'createdAt',r.created_at)) FROM trip_ratings r JOIN trips t ON t.id=r.trip_id WHERE r.subject_id=$1::UUID AND t.rider_id=$1::UUID AND r.actor_id=t.assigned_driver_id AND t.status='completed'),'[]'::JSONB),
 'noShowDebts',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'amountKobo',amount_kobo,'status',status,'createdAt',created_at,'settledAt',settled_at)) FROM rider_no_show_debts WHERE rider_id=$1::UUID),'[]'::JSONB),
 'supportCases',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'reference','HG-'||LPAD(c.case_number::TEXT,8,'0'),'tripId',c.trip_id,'subject',c.subject,'status',c.status,'createdAt',c.created_at,'messages',COALESCE((SELECT jsonb_agg(jsonb_build_object('body',m.body,'fromRider',m.sender_kind='user','createdAt',m.created_at) ORDER BY m.created_at) FROM support_case_messages m WHERE m.case_id=c.id),'[]'::JSONB))) FROM support_cases c WHERE opened_by=$1::UUID AND audience='rider'),'[]'::JSONB),
 'sosIntakes',COALESCE((SELECT jsonb_agg(jsonb_build_object('tripId',s.trip_id,'createdAt',s.created_at,'status',i.status,'locationSource',s.location_source,'locationCapturedAt',s.location_recorded_at)) FROM sos_intakes s JOIN safety_incidents i ON i.id=s.incident_id JOIN trips t ON t.id=s.trip_id WHERE s.reporter_id=$1::UUID AND t.rider_id=$1::UUID),'[]'::JSONB)
 )::TEXT`, owner).Scan(&data)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	entry, err := writer.Create("heygo-rider-export.json")
	if err != nil {
		return "", err
	}
	if _, err = entry.Write(data); err != nil {
		return "", err
	}
	rows, err := pool.Query(ctx, `SELECT a.storage_key,a.id::TEXT FROM support_case_attachments a JOIN support_case_messages m ON m.id=a.message_id JOIN support_cases c ON c.id=m.case_id WHERE c.opened_by=$1::UUID AND c.audience='rider'`, owner)
	if err != nil {
		return "", err
	}
	attachments := []struct{ key, id string }{}
	for rows.Next() {
		var item struct{ key, id string }
		if err := rows.Scan(&item.key, &item.id); err != nil {
			rows.Close()
			return "", err
		}
		attachments = append(attachments, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, item := range attachments {
		reader, err := files.Get(ctx, item.key)
		if err != nil {
			return "", err
		}
		entry, err := writer.Create("support-attachments/" + item.id)
		if err == nil {
			_, err = io.Copy(entry, reader)
		}
		reader.Close()
		if err != nil {
			return "", err
		}
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	key := fmt.Sprintf("rider-data-exports/%s/%s.zip", owner, exportID)
	if err = files.Put(ctx, key, "application/zip", bytes.NewReader(out.Bytes()), int64(out.Len())); err != nil {
		return "", err
	}
	return key, nil
}
