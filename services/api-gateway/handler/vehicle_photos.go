package handler

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

var vehiclePhotoAngles = []string{"front", "rear", "left", "right", "front_interior", "rear_interior", "vin", "walkaround"}

const maxVehiclePhotoBytes int64 = 10 * 1024 * 1024

func validateInspectionEvidence(ctx *gin.Context, tx pgx.Tx, driver, inspection string) error {
	var version int
	var revision int64
	var snapshot json.RawMessage
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT evidence_version,vehicle_revision,vehicle_snapshot FROM driver_inspections WHERE id=$1::uuid AND driver_id=$2::uuid`, inspection, driver).Scan(&version, &revision, &snapshot); err != nil {
		return fmt.Errorf("inspection evidence is unavailable")
	}
	if version == 0 {
		return nil
	} // Existing reviews retain their historical contract.
	var record struct {
		Findings VehicleFindings `json:"findings"`
	}
	if json.Unmarshal(snapshot, &record) != nil {
		return fmt.Errorf("inspection snapshot is invalid")
	}
	for _, check := range vehicleConditionChecks {
		if record.Findings.Checks[check] != "pass" {
			return fmt.Errorf("all safety checks must pass; %s is incomplete or failed", check)
		}
	}
	var count int
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT COUNT(DISTINCT angle) FROM driver_vehicle_photos WHERE driver_id=$1::uuid AND vehicle_revision=$2 AND superseded_at IS NULL`, driver, revision).Scan(&count); err != nil || count != len(vehiclePhotoAngles) {
		return fmt.Errorf("seven photos and a 1–2 minute walkaround video are required for this inspected vehicle revision")
	}
	return nil
}
func (a *adminAPI) reserveVehiclePhoto(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	if a.files == nil {
		driverError(ctx, 503, "storage_unavailable", "Photo storage is unavailable")
		return
	}
	var body struct {
		Angle           string `json:"angle"`
		ContentType     string `json:"contentType"`
		SizeBytes       int64  `json:"sizeBytes"`
		VehicleRevision int64  `json:"vehicleRevision"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !oneOf(body.Angle, vehiclePhotoAngles...) || !validVehicleMedia(body.Angle, body.ContentType) || body.SizeBytes <= 0 || body.SizeBytes > maxVehicleEvidenceBytes(body.Angle) {
		driverError(ctx, 400, "invalid_vehicle_photo", "Choose an image up to 10 MB or a walkaround MP4/MOV up to 200 MB")
		return
	}
	var revision int64
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT inspection_revision FROM driver_vehicles WHERE driver_id=$1::uuid`, driver).Scan(&revision); err != nil {
		driverError(ctx, 404, "vehicle_not_found", "Save vehicle details first")
		return
	}
	if revision != body.VehicleRevision {
		driverError(ctx, 409, "vehicle_changed", "Refresh the inspected vehicle before uploading")
		return
	}
	id := uuid.New()
	key := fmt.Sprintf("vehicle-photo-uploads/%s/%s", driver, id)
	expires := time.Now().UTC().Add(uploadLifetime)
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO vehicle_photo_uploads(id,driver_id,actor_id,actor_type,vehicle_revision,angle,storage_key,content_type,size_bytes,expires_at) VALUES($1,$2::uuid,$3::uuid,$10,$4,$5,$6,$7,$8,$9)`, id, driver, vehiclePhotoActor(ctx), revision, body.Angle, key, body.ContentType, body.SizeBytes, expires, vehiclePhotoActorType(ctx))
	if err != nil {
		reviewFailure(ctx)
		return
	}
	url, headers, err := a.files.PresignPut(ctx.Request.Context(), key, body.ContentType, body.SizeBytes, uploadLifetime)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(200, contracts.APIResponse{Data: gin.H{"uploadId": id, "url": url, "method": "PUT", "headers": headers, "expiresAt": expires}})
}
func (a *adminAPI) submitVehiclePhoto(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	if a.files == nil {
		driverError(ctx, 503, "storage_unavailable", "Photo storage is unavailable")
		return
	}
	var body struct {
		UploadID string `json:"uploadId"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, 400, "invalid_upload", "Upload ID required")
		return
	}
	if _, err := uuid.Parse(body.UploadID); err != nil {
		driverError(ctx, 400, "invalid_upload", "Valid upload ID required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var revision int64
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT inspection_revision FROM driver_vehicles WHERE driver_id=$1::uuid FOR UPDATE`, driver).Scan(&revision); err != nil {
		driverError(ctx, 404, "vehicle_not_found", "Vehicle unavailable")
		return
	}
	var key, angle, contentType string
	var size, uploadRevision int64
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT storage_key,angle,content_type,size_bytes,vehicle_revision,expires_at,consumed_at FROM vehicle_photo_uploads WHERE id=$1::uuid AND driver_id=$2::uuid AND actor_id=$3::uuid AND actor_type=$4 FOR UPDATE`, body.UploadID, driver, vehiclePhotoActor(ctx), vehiclePhotoActorType(ctx)).Scan(&key, &angle, &contentType, &size, &uploadRevision, &expires, &consumed)
	if errors.Is(err, pgx.ErrNoRows) || consumed != nil || !expires.After(time.Now()) || revision != uploadRevision {
		driverError(ctx, 409, "invalid_upload", "Upload expired, used, or belongs to another vehicle revision")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	var frozen bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_inspections WHERE driver_id=$1::uuid AND vehicle_revision=$2)`, driver, revision).Scan(&frozen)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if frozen {
		driverError(ctx, 409, "vehicle_evidence_locked", "Save a new vehicle revision before replacing submitted evidence")
		return
	}
	info, err := a.files.Head(ctx.Request.Context(), key)
	if err != nil || info.Size != size || info.ContentType != contentType {
		driverError(ctx, 400, "upload_incomplete", "Photo does not match its reservation")
		return
	}
	source, err := a.files.Get(ctx.Request.Context(), key)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	temp, err := os.CreateTemp("", "vehicle-evidence-*")
	if err != nil {
		source.Close()
		reviewFailure(ctx)
		return
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	copied, err := io.Copy(temp, io.LimitReader(source, maxVehicleEvidenceBytes(angle)+1))
	source.Close()
	if err != nil || copied != size {
		driverError(ctx, 400, "invalid_photo", "Evidence upload is incomplete")
		return
	}
	if _, err = temp.Seek(0, 0); err != nil {
		reviewFailure(ctx)
		return
	}
	if angle == "walkaround" {
		duration, e := vehicleVideoDuration(temp, size)
		if e != nil || duration < 60 || duration > 120 {
			driverError(ctx, 400, "invalid_video", "Walkaround must be a valid MP4/MOV video lasting 1–2 minutes")
			return
		}
	} else {
		cfg, format, e := image.DecodeConfig(temp)
		if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40000000 || ("image/"+format) != contentType {
			driverError(ctx, 400, "invalid_photo", "Photo must be a valid JPEG or PNG up to 40 megapixels")
			return
		}
	}
	if _, err = temp.Seek(0, 0); err != nil {
		reviewFailure(ctx)
		return
	}
	id := uuid.New()
	finalKey := fmt.Sprintf("vehicle-photos/%s/%s", driver, id)
	if err = a.files.Put(ctx.Request.Context(), finalKey, contentType, temp, size); err != nil {
		reviewFailure(ctx)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = a.files.Delete(ctx.Request.Context(), finalKey)
		}
	}()
	if _, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_vehicle_photos SET superseded_at=NOW() WHERE driver_id=$1::uuid AND vehicle_revision=$2 AND angle=$3 AND superseded_at IS NULL`, driver, revision, angle); err != nil {
		reviewFailure(ctx)
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_vehicle_photos(id,driver_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type) VALUES($1,$2::uuid,$3,$4,$5,$6,$7,$8::uuid,$9)`, id, driver, revision, angle, finalKey, contentType, size, vehiclePhotoActor(ctx), vehiclePhotoActorType(ctx))
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE vehicle_photo_uploads SET consumed_at=NOW() WHERE id=$1::uuid`, body.UploadID)
	}
	if err == nil {
		if vehiclePhotoActorType(ctx) == "admin" {
			err = adminAudit(ctx, tx, "vehicle.photo.submitted", driver, gin.H{"photoId": id, "angle": angle, "vehicleRevision": revision})
		} else {
			_, err = insertDriverEvent(ctx, tx, driver, "driver.vehicle.photo.submitted", gin.H{"photoId": id, "angle": angle, "vehicleRevision": revision})
		}
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	committed = true
	ctx.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "angle": angle, "vehicleRevision": revision}})
}
func signedVehiclePhotos(ctx *gin.Context, pool *pgxpool.Pool, files storage.ObjectStore, driver string, revision int64) ([]gin.H, error) {
	photos := []gin.H{}
	rows, err := pool.Query(ctx.Request.Context(), `SELECT id::text,angle,storage_key,content_type FROM driver_vehicle_photos WHERE driver_id=$1::uuid AND vehicle_revision=$2 AND superseded_at IS NULL ORDER BY CASE WHEN angle='front' THEN 0 ELSE 1 END,angle`, driver, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, angle, key, contentType string
		if err := rows.Scan(&id, &angle, &key, &contentType); err != nil {
			return nil, err
		}
		if files == nil {
			continue
		}
		url, err := files.PresignGet(ctx.Request.Context(), key, 2*time.Minute)
		if err != nil {
			return nil, err
		}
		photos = append(photos, gin.H{"id": id, "angle": angle, "contentType": contentType, "url": url, "expiresAt": time.Now().UTC().Add(2 * time.Minute)})
	}
	return photos, rows.Err()
}
func (a *adminAPI) vehiclePhotos(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var revision int64
	if raw := ctx.Query("revision"); raw != "" {
		var err error
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 0 {
			driverError(ctx, 400, "invalid_revision", "Valid vehicle revision required")
			return
		}
	} else if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT inspection_revision FROM driver_vehicles WHERE driver_id=$1::uuid`, driver).Scan(&revision); err != nil {
		driverError(ctx, 404, "vehicle_not_found", "Vehicle unavailable")
		return
	}
	photos, err := signedVehiclePhotos(ctx, a.pool, a.files, driver, revision)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	var locked bool
	if err = a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_inspections WHERE driver_id=$1::uuid AND vehicle_revision=$2)`, driver, revision).Scan(&locked); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"vehicleRevision": revision, "photos": photos, "locked": locked}})
}

func vehiclePhotoActor(ctx *gin.Context) string {
	if id, ok := ctx.Get("vehiclePhotoDriverActor"); ok {
		return id.(string)
	}
	return currentAdmin(ctx).ID
}
func vehiclePhotoActorType(ctx *gin.Context) string {
	if _, ok := ctx.Get("vehiclePhotoDriverActor"); ok {
		return "driver"
	}
	return "admin"
}
func (a *driverAPI) ownVehiclePhotos(ctx *gin.Context) {
	ctx.Params = append(ctx.Params, gin.Param{Key: "driverID", Value: driverID(ctx)})
	(&adminAPI{pool: a.pool, files: a.files}).vehiclePhotos(ctx)
}
func (a *driverAPI) reserveOwnVehiclePhoto(ctx *gin.Context) {
	ctx.Set("vehiclePhotoDriverActor", driverID(ctx))
	ctx.Params = append(ctx.Params, gin.Param{Key: "driverID", Value: driverID(ctx)})
	(&adminAPI{pool: a.pool, files: a.files}).reserveVehiclePhoto(ctx)
}
func (a *driverAPI) submitOwnVehiclePhoto(ctx *gin.Context) {
	ctx.Set("vehiclePhotoDriverActor", driverID(ctx))
	ctx.Params = append(ctx.Params, gin.Param{Key: "driverID", Value: driverID(ctx)})
	(&adminAPI{pool: a.pool, files: a.files}).submitVehiclePhoto(ctx)
}
func maxVehicleEvidenceBytes(angle string) int64 {
	if angle == "walkaround" {
		return 200 * 1024 * 1024
	}
	return maxVehiclePhotoBytes
}
func validVehicleMedia(angle, contentType string) bool {
	if angle == "walkaround" {
		return oneOf(contentType, "video/mp4", "video/quicktime")
	}
	return oneOf(contentType, "image/jpeg", "image/png")
}

// Read bounded ISO-BMFF atoms from the file; never load a video into memory.
func vehicleVideoDuration(r io.ReaderAt, size int64) (float64, error) {
	var duration float64
	var hasMovie, hasVideo, hasMedia bool
	atoms := 0
	var walk func(int64, int64, int) error
	walk = func(start, end int64, depth int) error {
		if depth > 4 {
			return fmt.Errorf("invalid media nesting")
		}
		for offset := start; offset+8 <= end; {
			atoms++
			if atoms > 4096 {
				return fmt.Errorf("too many media atoms")
			}
			var header [16]byte
			if _, err := r.ReadAt(header[:8], offset); err != nil {
				return err
			}
			atomSize := int64(binary.BigEndian.Uint32(header[:4]))
			headerSize := int64(8)
			if atomSize == 1 {
				if _, err := r.ReadAt(header[8:], offset+8); err != nil {
					return err
				}
				big := binary.BigEndian.Uint64(header[8:])
				if big > uint64(end-offset) {
					return fmt.Errorf("invalid atom size")
				}
				atomSize = int64(big)
				headerSize = 16
			}
			if atomSize == 0 {
				atomSize = end - offset
			}
			if atomSize < headerSize || atomSize > end-offset {
				return fmt.Errorf("invalid media atom")
			}
			kind := string(header[4:8])
			payload := offset + headerSize
			switch {
			case kind == "mdat" && depth == 0:
				hasMedia = atomSize > headerSize
			case kind == "moov" && depth == 0:
				if err := walk(payload, offset+atomSize, 1); err != nil {
					return err
				}
			case kind == "trak" && depth == 1:
				if err := walk(payload, offset+atomSize, 2); err != nil {
					return err
				}
			case kind == "mdia" && depth == 2:
				if err := walk(payload, offset+atomSize, 3); err != nil {
					return err
				}
			case kind == "hdlr" && depth == 3:
				if atomSize-headerSize < 12 {
					return fmt.Errorf("invalid media handler")
				}
				var b [12]byte
				if _, err := r.ReadAt(b[:], payload); err != nil {
					return err
				}
				if string(b[8:12]) == "vide" {
					hasVideo = true
				}
			case kind == "mvhd" && depth == 1:
				var b [32]byte
				n := int64(len(b))
				if atomSize-headerSize < n {
					n = atomSize - headerSize
				}
				if n < 20 {
					return fmt.Errorf("short movie header")
				}
				if _, err := r.ReadAt(b[:n], payload); err != nil {
					return err
				}
				var scale uint32
				var ticks uint64
				if b[0] == 0 {
					scale = binary.BigEndian.Uint32(b[12:16])
					ticks = uint64(binary.BigEndian.Uint32(b[16:20]))
				} else if b[0] == 1 && n >= 32 {
					scale = binary.BigEndian.Uint32(b[20:24])
					ticks = binary.BigEndian.Uint64(b[24:32])
				} else {
					return fmt.Errorf("invalid movie version")
				}
				if scale == 0 {
					return fmt.Errorf("invalid timescale")
				}
				duration = float64(ticks) / float64(scale)
				hasMovie = true
			}
			offset += atomSize
		}
		return nil
	}
	if err := walk(0, size, 0); err != nil {
		return 0, err
	}
	if !hasMovie || !hasVideo || !hasMedia {
		return 0, fmt.Errorf("video track, duration or media data missing")
	}
	return duration, nil
}

// Temporary upload keys can only be removed after their signed PUTs have expired.
func expireVehiclePhotoUpload(ctx context.Context, pool *pgxpool.Pool, files storage.ObjectStore) bool {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var id, key string
	err = tx.QueryRow(ctx, `SELECT id::text,storage_key FROM vehicle_photo_uploads WHERE expires_at<NOW()-INTERVAL '1 hour' ORDER BY expires_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &key)
	if err != nil {
		return false
	}
	if files.Delete(ctx, key) != nil {
		return false
	}
	if _, err = tx.Exec(ctx, `DELETE FROM vehicle_photo_uploads WHERE id=$1::uuid`, id); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
