package handler

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

const driverExportLifetime = 7 * 24 * time.Hour

func registerDriverPrivacyRoutes(group *gin.RouterGroup, api *driverAPI) {
	group.POST("/data-export", api.requestDriverDataExport)
	group.GET("/data-export/:exportID", api.getDriverDataExport)
	group.POST("/account-deletion", api.requestDriverAccountDeletion)
	group.GET("/account-deletion", api.getDriverAccountDeletion)
}

func (a *driverAPI) requestDriverDataExport(ctx *gin.Context) {
	user, _ := gatewayauth.CurrentUser(ctx)
	if !user.EmailVerified || !validResendEmail(user.Email) {
		driverError(ctx, http.StatusConflict, "verified_email_required", "A verified account email is required for a data export")
		return
	}
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "private_storage_unavailable", "Private archive storage is not configured")
		return
	}
	var resendConfigured bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM admin_resend_settings WHERE id=1)`).Scan(&resendConfigured); err != nil || !resendConfigured {
		driverError(ctx, http.StatusServiceUnavailable, "email_delivery_unavailable", "Private archive email delivery is not configured")
		return
	}
	id := uuid.NewString()
	var createdAt time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `INSERT INTO driver_data_exports(id,driver_id,requested_email)
		VALUES($1::UUID,$2::UUID,$3) ON CONFLICT(driver_id) WHERE status IN ('pending','processing') DO NOTHING
		RETURNING id::TEXT,requested_at`, id, user.ID, user.Email).Scan(&id, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,requested_at FROM driver_data_exports
			WHERE driver_id=$1::UUID AND status IN ('pending','processing')`, user.ID).Scan(&id, &createdAt)
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "data_export_unavailable", "The data export request could not be recorded")
		return
	}
	ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"id": id, "status": "pending", "requestedAt": createdAt}})
}

func (a *driverAPI) getDriverDataExport(ctx *gin.Context) {
	exportID, err := uuid.Parse(ctx.Param("exportID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_export_id", "A valid export ID is required")
		return
	}
	var status string
	var requestedAt time.Time
	var sentAt, expiresAt *time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT status,requested_at,sent_at,expires_at FROM driver_data_exports WHERE id=$1::UUID AND driver_id=$2::UUID`, exportID, driverID(ctx)).Scan(&status, &requestedAt, &sentAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "data_export_not_found", "Data export request was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "data_export_unavailable", "The data export status is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": exportID.String(), "status": status, "requestedAt": requestedAt, "sentAt": sentAt, "expiresAt": expiresAt}})
}

func (a *driverAPI) requestDriverAccountDeletion(ctx *gin.Context) {
	id := uuid.NewString()
	var requestedAt time.Time
	user, _ := gatewayauth.CurrentUser(ctx)
	err := a.pool.QueryRow(ctx.Request.Context(), `INSERT INTO driver_account_deletion_requests(id,driver_id,casper_subject_hash,human_id_hash,phone_hash,email_hash)
		VALUES($1::UUID,$2::UUID,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,'')) ON CONFLICT(driver_id) WHERE status='pending' DO NOTHING RETURNING requested_at`,
		id, user.ID, hashIdentityForPrivacy(user.CasperSubject), hashIdentityForPrivacy(user.HumanID), hashIdentityForPrivacy(user.Phone), hashIdentityForPrivacy(user.Email)).Scan(&requestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,requested_at FROM driver_account_deletion_requests WHERE driver_id=$1::UUID AND status='pending'`, driverID(ctx)).Scan(&id, &requestedAt)
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The account deletion request could not be recorded")
		return
	}
	ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"id": id, "status": "pending", "requestedAt": requestedAt}})
}

func hashIdentityForPrivacy(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *driverAPI) getDriverAccountDeletion(ctx *gin.Context) {
	var id, status string
	var requestedAt time.Time
	var reviewedAt *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,status,requested_at,reviewed_at FROM driver_account_deletion_requests
		WHERE driver_id=$1::UUID ORDER BY requested_at DESC,id DESC LIMIT 1`, driverID(ctx)).Scan(&id, &status, &requestedAt, &reviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"request": nil}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "account_deletion_unavailable", "The account deletion status is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"request": gin.H{"id": id, "status": status, "requestedAt": requestedAt, "reviewedAt": reviewedAt}}})
}

type exportAttachment struct {
	Key         string
	Name        string
	ContentType string
}

func buildDriverDataArchive(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, driverID, exportID string) (string, error) {
	var payload []byte
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'generatedAt',NOW(),
		'profile',(SELECT to_jsonb(p)-'driver_id' FROM driver_profiles p WHERE p.driver_id=$1::UUID),
		'vehicle',(SELECT to_jsonb(v)-'driver_id' FROM driver_vehicles v WHERE v.driver_id=$1::UUID),
		'notificationPreferences',(SELECT to_jsonb(p)-'driver_id' FROM driver_notification_preferences p WHERE p.driver_id=$1::UUID),
		'residentialAddress',(SELECT residential_address FROM driver_identity_profiles WHERE driver_id=$1::uuid),
		'homeDestination',(SELECT to_jsonb(h)-'driver_id'-'location' FROM driver_home_destinations h WHERE h.driver_id=$1::uuid),
        'ridePreferences',(SELECT to_jsonb(p)-'driver_id' FROM driver_ride_preferences p WHERE p.driver_id=$1::UUID),
 'additionalSafetyContacts',COALESCE((SELECT jsonb_agg(to_jsonb(c)-'driver_id') FROM driver_additional_safety_contacts c WHERE c.driver_id=$1::UUID),'[]'::JSONB),
		'safetyContact',(SELECT to_jsonb(c)-'driver_id' FROM driver_safety_contacts c WHERE c.driver_id=$1::UUID),
		'documents',COALESCE((SELECT jsonb_agg(to_jsonb(d)-'storage_key') FROM driver_documents d WHERE d.driver_id=$1::UUID),'[]'::JSONB),
		'vehiclePhotos',COALESCE((SELECT jsonb_agg(to_jsonb(p)-'storage_key'-'uploaded_by') FROM driver_vehicle_photos p WHERE p.driver_id=$1::UUID),'[]'::JSONB),
        'inspections',COALESCE((SELECT jsonb_agg(to_jsonb(i)) FROM driver_inspections i WHERE i.driver_id=$1::UUID),'[]'::JSONB),
		'trips',COALESCE((SELECT jsonb_agg(jsonb_build_object('trip',to_jsonb(t),'fare',to_jsonb(f),'earnings',to_jsonb(e),'settlement',to_jsonb(s)) ORDER BY t.created_at)
			FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id LEFT JOIN driver_trip_earnings e ON e.trip_id=t.id LEFT JOIN trip_settlements s ON s.trip_id=t.id
			WHERE t.assigned_driver_id=$1::UUID),'[]'::JSONB),
		'ratings',COALESCE((SELECT jsonb_agg(to_jsonb(r)-'actor_id') FROM trip_ratings r WHERE r.subject_id=$1::UUID),'[]'::JSONB),
		'operatingBalance',COALESCE((SELECT to_jsonb(x) FROM driver_operating_accounts x WHERE x.driver_id=$1::UUID),'null'::JSONB),
		'operatingBalanceEntries',COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.created_at) FROM driver_operating_entries x WHERE x.driver_id=$1::UUID),'[]'::JSONB),
		'operatingBalanceTopups',COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.created_at) FROM driver_operating_topups x WHERE x.driver_id=$1::UUID),'[]'::JSONB),
		'noShowClaims',COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.submitted_at) FROM driver_no_show_claims x WHERE x.driver_id=$1::UUID),'[]'::JSONB)
	)::TEXT`, driverID).Scan(&payload)
	if err != nil {
		return "", fmt.Errorf("read Driver archive data: %w", err)
	}
	var attachments []exportAttachment
	rows, err := pool.Query(ctx, `SELECT storage_key,'documents/'||type||'-'||id::TEXT, 'application/octet-stream' FROM driver_documents WHERE driver_id=$1::UUID
		UNION ALL SELECT storage_key,'documents/upload-'||id::TEXT,content_type FROM driver_document_uploads WHERE driver_id=$1::UUID
		UNION ALL SELECT storage_key,'vehicle-photos/'||angle||'-'||id::text,content_type FROM driver_vehicle_photos WHERE driver_id=$1::uuid
        UNION ALL SELECT report_storage_key,'inspection-reports/'||id::TEXT,'application/octet-stream' FROM driver_inspections WHERE driver_id=$1::UUID AND report_storage_key IS NOT NULL`, driverID)
	if err != nil {
		return "", fmt.Errorf("read Driver archive files: %w", err)
	}
	for rows.Next() {
		var item exportAttachment
		if err := rows.Scan(&item.Key, &item.Name, &item.ContentType); err != nil {
			rows.Close()
			return "", err
		}
		attachments = append(attachments, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	temp, err := os.CreateTemp("", "heygo-driver-export-*.zip")
	if err != nil {
		return "", fmt.Errorf("create private export archive: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	zipWriter := zip.NewWriter(temp)
	manifest, err := zipWriter.Create("heygo-driver-export.json")
	if err == nil {
		_, err = manifest.Write(payload)
	}
	for _, attachment := range attachments {
		if err != nil {
			break
		}
		reader, getErr := files.Get(ctx, attachment.Key)
		if getErr != nil {
			err = fmt.Errorf("read private export attachment: %w", getErr)
			break
		}
		entry, createErr := zipWriter.Create(filepath.ToSlash(attachment.Name))
		if createErr == nil {
			_, createErr = io.Copy(entry, reader)
		}
		closeErr := reader.Close()
		if createErr != nil {
			err = createErr
		} else if closeErr != nil {
			err = closeErr
		}
	}
	if closeErr := zipWriter.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = temp.Close()
		return "", err
	}
	info, err := temp.Stat()
	if err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		_ = temp.Close()
		return "", err
	}
	key := "driver-data-exports/" + driverID + "/" + exportID + ".zip"
	err = files.Put(ctx, key, "application/zip", temp, info.Size())
	closeErr := temp.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return key, nil
}

func processNextDriverDataExport(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, encryptionKey []byte) bool {
	return processNextDriverDataExportUsingSender(ctx, pool, files, encryptionKey, sendResendEmailWithKey)
}

type privacyEmailSender func(context.Context, string, string, string, string, string, string, string, string) error

func processNextDriverDataExportUsingSender(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, encryptionKey []byte, send privacyEmailSender) bool {
	return processNextPrivateDataExport(ctx, pool, files, encryptionKey, send, "driver")
}
func processNextPrivateDataExport(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, encryptionKey []byte, send privacyEmailSender, scope string) bool {
	table, ownerColumn := "driver_data_exports", "driver_id"
	if scope == "rider" {
		table, ownerColumn = "rider_data_exports", "rider_id"
	} else if scope != "driver" {
		return false
	}
	sql := func(query string) string {
		return strings.ReplaceAll(strings.ReplaceAll(query, "driver_data_exports", table), "driver_id", ownerColumn)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	var id, driverID, recipient string
	var storedArchiveKey, storedFromEmail, storedFromName *string
	var storedLinkCiphertext []byte
	var storedExpiresAt *time.Time
	err = tx.QueryRow(ctx, sql(`SELECT id::TEXT,driver_id::TEXT,requested_email,archive_storage_key,archive_link_ciphertext,mail_from_email,mail_from_name,expires_at
		FROM driver_data_exports WHERE (status='pending' AND next_attempt_at<=NOW()) OR (status='processing' AND lease_until<NOW())
		ORDER BY requested_at FOR UPDATE SKIP LOCKED LIMIT 1`)).Scan(&id, &driverID, &recipient, &storedArchiveKey, &storedLinkCiphertext, &storedFromEmail, &storedFromName, &storedExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return false
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return false
	}
	_, err = tx.Exec(ctx, sql(`UPDATE driver_data_exports SET status='processing',attempt_count=attempt_count+1,lease_until=NOW()+INTERVAL '5 minutes',updated_at=NOW() WHERE id=$1::UUID`), id)
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		return true
	}
	var active bool
	if err := pool.QueryRow(ctx, sql(`SELECT active FROM users WHERE id=$1::UUID`), driverID).Scan(&active); err != nil || !active {
		_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET status='cancelled',requested_email='',lease_until=NULL,updated_at=NOW() WHERE id=$1::UUID AND status='processing'`), id)
		return true
	}
	settings, configured, settingsErr := readResendSettingsPool(ctx, pool)
	var key, link string
	fromEmail, fromName := settings.FromEmail, settings.FromName
	if settingsErr != nil || !configured || len(encryptionKey) != 32 {
		err = fmt.Errorf("Resend is not configured")
	} else if apiKey, decryptErr := openAdminSecret(encryptionKey, settings.APIKeyCiphertext); decryptErr != nil {
		err = fmt.Errorf("Resend settings cannot be opened")
	} else {
		if storedExpiresAt != nil && !storedExpiresAt.After(time.Now()) {
			err = fmt.Errorf("export link expired")
		}
		if err == nil && storedArchiveKey != nil {
			key = *storedArchiveKey
		} else if err == nil {
			if scope == "rider" {
				key, err = buildRiderDataArchive(ctx, pool, files, driverID, id)
			} else {
				key, err = buildDriverDataArchive(ctx, pool, files, driverID, id)
			}
			if err == nil {
				var result pgconn.CommandTag
				result, err = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET archive_storage_key=$2 WHERE id=$1::UUID AND status='processing'`), id, key)
				if err == nil && result.RowsAffected() == 0 {
					_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET archive_storage_key=$2,expires_at=NOW() WHERE id=$1::UUID AND status IN ('cancelled','failed')`), id, key)
					err = fmt.Errorf("export request was cancelled")
				}
			}
		}
		if err == nil && len(storedLinkCiphertext) > 0 {
			link, err = openAdminValue(encryptionKey, storedLinkCiphertext, "heygo-data-export-link-v1")
			if storedFromEmail == nil || storedFromName == nil {
				err = fmt.Errorf("stored archive email is incomplete")
			} else {
				fromEmail, fromName = *storedFromEmail, *storedFromName
			}
		} else if err == nil {
			var presigned string
			presigned, err = files.PresignGet(ctx, key, driverExportLifetime)
			if err == nil {
				expires := time.Now().UTC().Add(driverExportLifetime)
				storedExpiresAt = &expires
				var cipherText []byte
				cipherText, err = sealAdminValue(encryptionKey, presigned, "heygo-data-export-link-v1")
				if err == nil {
					var result pgconn.CommandTag
					result, err = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET archive_link_ciphertext=$2,mail_from_email=$3,mail_from_name=$4,expires_at=$5
						WHERE id=$1::UUID AND status='processing'`), id, cipherText, fromEmail, fromName, storedExpiresAt)
					if err == nil && result.RowsAffected() == 0 {
						err = fmt.Errorf("export request was cancelled")
					}
					link = presigned
				}
			}
		}
		if err == nil {
			var stillEligible bool
			if checkErr := pool.QueryRow(ctx, sql(`SELECT EXISTS(SELECT 1 FROM driver_data_exports e JOIN users u ON u.id=e.driver_id WHERE e.id=$1::UUID AND e.status='processing' AND u.active=TRUE AND ($2='driver' OR (u.email_verified=TRUE AND LOWER(u.email)=LOWER(e.requested_email))))`), id, scope).Scan(&stillEligible); checkErr != nil || !stillEligible {
				err = fmt.Errorf("export request was cancelled")
			} else {
				roleName := "Driver"
				if scope == "rider" {
					roleName = "Rider"
				}
				textBody := "Your HeyGo " + roleName + " data archive is ready. This private download link expires in 7 days: " + link
				htmlBody := "<p>Your HeyGo " + roleName + " data archive is ready.</p><p>This private download link expires in 7 days: <a href=\"" + html.EscapeString(link) + "\">Download your archive</a></p>"
				err = send(ctx, scope+"-data-export/"+id, apiKey, fromEmail, fromName, recipient, "Your HeyGo data archive", textBody, htmlBody)
			}
		}
	}
	if err != nil {
		_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET status=CASE WHEN attempt_count>=5 OR $2='export link expired' THEN 'failed' WHEN $2='export request was cancelled' THEN 'cancelled' ELSE 'pending' END,
			requested_email=CASE WHEN attempt_count>=5 OR $2 IN ('export request was cancelled','export link expired') THEN '' ELSE requested_email END,
			archive_link_ciphertext=CASE WHEN attempt_count>=5 OR $2 IN ('export request was cancelled','export link expired') THEN NULL ELSE archive_link_ciphertext END,
			expires_at=CASE WHEN attempt_count>=5 AND archive_storage_key IS NOT NULL THEN NOW() ELSE expires_at END,
			next_attempt_at=NOW()+make_interval(secs=>LEAST(3600,power(2,LEAST(attempt_count,10))::INTEGER)),
			lease_until=NULL,last_error='delivery_or_archive_failed',updated_at=NOW() WHERE id=$1::UUID AND status='processing'`), id, err.Error())
		return true
	}
	expiresAt := time.Now().UTC().Add(driverExportLifetime)
	if storedExpiresAt != nil {
		expiresAt = *storedExpiresAt
	}
	_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET status='sent',sent_at=NOW(),expires_at=$2,next_attempt_at=$2,
		lease_until=NULL,last_error='',updated_at=NOW() WHERE id=$1::UUID AND status='processing'`), id, expiresAt)
	return true
}

func readResendSettingsPool(ctx context.Context, pool *pgxpool.Pool) (resendSettings, bool, error) {
	var settings resendSettings
	err := pool.QueryRow(ctx, `SELECT api_key_ciphertext,from_email,from_name,updated_at FROM admin_resend_settings WHERE id=1`).Scan(&settings.APIKeyCiphertext, &settings.FromEmail, &settings.FromName, &settings.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return resendSettings{}, false, nil
	}
	return settings, err == nil, err
}

func expireDriverExports(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore) {
	expirePrivateExports(ctx, pool, files, "driver_data_exports")
}
func expirePrivateExports(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, table string) {
	if table != "driver_data_exports" && table != "rider_data_exports" {
		return
	}
	sql := func(query string) string { return strings.ReplaceAll(query, "driver_data_exports", table) }
	tx, err := pool.Begin(ctx)
	if err != nil {
		return
	}
	var id, key string
	err = tx.QueryRow(ctx, sql(`SELECT id::TEXT,archive_storage_key FROM driver_data_exports
		WHERE ((status='sent' AND expires_at<=NOW()) OR (status IN ('failed','cancelled') AND archive_storage_key IS NOT NULL))
		AND (lease_until IS NULL OR lease_until<NOW())
		FOR UPDATE SKIP LOCKED LIMIT 1`)).Scan(&id, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return
	}
	if _, err := tx.Exec(ctx, sql(`UPDATE driver_data_exports SET lease_until=NOW()+INTERVAL '2 minutes' WHERE id=$1::UUID`), id); err != nil || tx.Commit(ctx) != nil {
		_ = tx.Rollback(ctx)
		return
	}
	if files.Delete(ctx, key) == nil {
		_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET status=CASE WHEN status='sent' THEN 'expired' ELSE status END,archive_storage_key=NULL,
			archive_link_ciphertext=NULL,mail_from_email=NULL,mail_from_name=NULL,requested_email='',lease_until=NULL,updated_at=NOW() WHERE id=$1::UUID AND status IN ('sent','failed','cancelled')`), id)
	} else {
		_, _ = pool.Exec(ctx, sql(`UPDATE driver_data_exports SET lease_until=NULL WHERE id=$1::UUID`), id)
	}
}

func processDriverDeletionFile(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore) bool {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	var requestID, key string
	err = tx.QueryRow(ctx, `SELECT request_id::TEXT,storage_key FROM driver_account_deletion_files
		WHERE deleted_at IS NULL AND next_attempt_at<=NOW() AND (lease_until IS NULL OR lease_until<NOW())
		ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&requestID, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return false
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return false
	}
	_, err = tx.Exec(ctx, `UPDATE driver_account_deletion_files SET lease_until=NOW()+INTERVAL '2 minutes',attempts=attempts+1 WHERE request_id=$1::UUID AND storage_key=$2`, requestID, key)
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		return true
	}
	delErr := files.Delete(ctx, key)
	if delErr == nil {
		_, _ = pool.Exec(ctx, `UPDATE driver_account_deletion_files SET deleted_at=NOW(),lease_until=NULL,last_error='' WHERE request_id=$1::UUID AND storage_key=$2`, requestID, key)
	} else {
		_, _ = pool.Exec(ctx, `UPDATE driver_account_deletion_files SET next_attempt_at=NOW()+LEAST(INTERVAL '1 day',make_interval(secs=>LEAST(3600,power(2,LEAST(attempts,10))::INTEGER))),lease_until=NULL,last_error='storage_delete_failed' WHERE request_id=$1::UUID AND storage_key=$2`, requestID, key)
	}
	_, _ = pool.Exec(ctx, `UPDATE driver_account_deletion_requests r SET completed_at=NOW() WHERE r.id=$1::UUID AND r.status='approved' AND r.completed_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM driver_account_deletion_files f WHERE f.request_id=r.id AND f.deleted_at IS NULL)`, requestID)
	return true
}

func StartPrivacyJobs(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore) {
	if files == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		key, _ := hex.DecodeString(os.Getenv("HEYGO_ADMIN_SETTINGS_KEY"))
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jobCtx, cancel := context.WithTimeout(ctx, time.Minute)
				worked := processNextDriverDataExport(jobCtx, pool, files, key)
				riderWorked := processNextPrivateDataExport(jobCtx, pool, files, key, sendResendEmailWithKey, "rider")
				worked = worked || riderWorked
				if !worked {
					worked = processDriverDeletionFile(jobCtx, pool, files)
					if !worked {
						worked = expireVehiclePhotoUpload(jobCtx, pool, files)
					}
				}
				if !worked {
					expireDriverExports(jobCtx, pool, files)
					expirePrivateExports(jobCtx, pool, files, "rider_data_exports")
				}
				cancel()
			}
		}
	}()
}

// RunPrivacyJobsOnce performs a bounded maintenance pass suitable for an
// authenticated Cloud Scheduler request. Database leases keep overlapping
// requests safe, and the bound prevents a large backlog from exceeding the
// request deadline.
func RunPrivacyJobsOnce(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore, limit int) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if files == nil || limit <= 0 {
		return
	}
	key, _ := hex.DecodeString(os.Getenv("HEYGO_ADMIN_SETTINGS_KEY"))
	for i := range limit {
		scope := "driver"
		if i%2 == 1 {
			scope = "rider"
		}
		if processNextPrivateDataExport(ctx, pool, files, key, sendResendEmailWithKey, scope) {
			continue
		}
		other := "rider"
		if scope == "rider" {
			other = "driver"
		}
		if processNextPrivateDataExport(ctx, pool, files, key, sendResendEmailWithKey, other) {
			continue
		}
		if processDriverDeletionFile(ctx, pool, files) {
			continue
		}
		if expireVehiclePhotoUpload(ctx, pool, files) {
			continue
		}
		break
	}
	expireDriverExports(ctx, pool, files)
	expirePrivateExports(ctx, pool, files, "rider_data_exports")
}
