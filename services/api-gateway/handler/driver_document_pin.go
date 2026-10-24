package handler

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"os"
	"time"
)

func validDocumentPIN(pin string) bool {
	if len(pin) != 4 {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (a *driverAPI) documentPINStatus(c *gin.Context) {
	var configured bool
	if err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT pin_hash<>'' FROM driver_document_pins WHERE driver_id=$1::UUID),FALSE)`, driverID(c)).Scan(&configured); err != nil {
		driverError(c, 503, "pin_unavailable", "PIN settings unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"configured": configured}})
}

// A keyed digest prevents a stolen database hash being brute-forced using only 10,000 PIN values.
func documentPINDigest(driver, pin string) (string, bool) {
	pepper := os.Getenv("OTP_PEPPER")
	if len(pepper) < 32 {
		return "", false
	}
	return otpHash([]byte(pepper), driver, "driver-document-pin-v1", pin), true
}
func (a *driverAPI) authenticateDocumentPIN(c *gin.Context, pin string, newPIN *string) bool {
	return a.authenticatePINFor(c, driverID(c), pin, newPIN, nil)
}

// Reuse the same PIN, lockout and storage for documents and account security.
func (a *driverAPI) authenticatePINFor(c *gin.Context, owner, pin string, newPIN *string, after func(pgx.Tx) error) bool {
	if !validDocumentPIN(pin) && newPIN == nil {
		driverError(c, 400, "invalid_pin", "Enter your four-digit PIN")
		return false
	}
	digest, ready := documentPINDigest(owner, pin)
	if !ready {
		driverError(c, 503, "pin_unavailable", "PIN verification unavailable")
		return false
	}
	var newHash []byte
	if newPIN != nil {
		if !validDocumentPIN(*newPIN) {
			driverError(c, 400, "invalid_pin", "Use a four-digit PIN")
			return false
		}
		d, _ := documentPINDigest(owner, *newPIN)
		var err error
		newHash, err = bcrypt.GenerateFromPassword([]byte(d), bcrypt.DefaultCost)
		if err != nil {
			driverError(c, 503, "pin_unavailable", "PIN could not be saved")
			return false
		}
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "pin_unavailable", "PIN verification unavailable")
		return false
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_document_pins(driver_id) VALUES($1::UUID) ON CONFLICT DO NOTHING`, owner)
	var hash string
	var attempts int
	var locked *time.Time
	if err == nil {
		err = tx.QueryRow(c.Request.Context(), `SELECT pin_hash,failed_attempts,locked_until FROM driver_document_pins WHERE driver_id=$1::UUID FOR UPDATE`, owner).Scan(&hash, &attempts, &locked)
	}
	if err != nil {
		driverError(c, 503, "pin_unavailable", "PIN verification unavailable")
		return false
	}
	if locked != nil && locked.After(time.Now()) {
		driverError(c, 429, "pin_locked", "Too many incorrect PINs. Try again in 15 minutes.")
		return false
	}
	if locked != nil {
		attempts = 0
	}
	if hash == "" && newPIN == nil {
		driverError(c, 409, "pin_required", "Set a PIN before viewing documents")
		return false
	}
	if hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(digest)) != nil {
		attempts++
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_document_pins SET failed_attempts=$2,locked_until=CASE WHEN $2>=5 THEN NOW()+INTERVAL '15 minutes' ELSE NULL END WHERE driver_id=$1::UUID`, owner, attempts)
		if err == nil {
			err = tx.Commit(c.Request.Context())
		}
		if err != nil {
			driverError(c, 503, "pin_unavailable", "PIN verification unavailable")
			return false
		}
		if attempts >= 5 {
			driverError(c, 429, "pin_locked", "Too many incorrect PINs. Try again in 15 minutes.")
		} else {
			driverError(c, 403, "incorrect_pin", "Incorrect PIN")
		}
		return false
	}
	if newPIN != nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_document_pins SET pin_hash=$2,failed_attempts=0,locked_until=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, owner, string(newHash))
	} else {
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_document_pins SET failed_attempts=0,locked_until=NULL WHERE driver_id=$1::UUID`, owner)
	}
	if err == nil && after != nil {
		err = after(tx)
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "pin_unavailable", "PIN verification unavailable")
		return false
	}
	return true
}
func (a *driverAPI) setDocumentPIN(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var body struct {
		Current string `json:"currentPin"`
		New     string `json:"newPin"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_pin", "Enter a valid PIN")
		return
	}
	if !a.authenticateDocumentPIN(c, body.Current, &body.New) {
		return
	}
	a.documentPINStatus(c)
}
func (a *driverAPI) viewApprovedDocument(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var body struct {
		PIN string `json:"pin"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_pin", "Enter your PIN")
		return
	}
	kind := c.Param("id")
	var status string
	var metadata []byte
	var key, docType string
	var expires *time.Time
	fields := gin.H{}
	files := []gin.H{}
	if kind == "identity" {
		err := a.pool.QueryRow(c.Request.Context(), `SELECT manual_verification_status FROM driver_profiles WHERE driver_id=$1::UUID`, driverID(c)).Scan(&status)
		if err != nil || status != "approved" {
			driverError(c, 409, "document_locked", "This document is not approved for viewing")
			return
		}
	} else if kind == "inspection" {
		err := a.pool.QueryRow(c.Request.Context(), `SELECT status,COALESCE(report_storage_key,''),partner_name,location_name FROM driver_inspections WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, driverID(c)).Scan(&status, &key, new(string), new(string))
		if err != nil || status != "completed" {
			driverError(c, 409, "document_locked", "This inspection is not approved for viewing")
			return
		}
	} else {
		id, err := uuid.Parse(kind)
		if err != nil {
			driverError(c, 400, "invalid_document", "Invalid document")
			return
		}
		err = a.pool.QueryRow(c.Request.Context(), `SELECT status,metadata,storage_key,type,expires_at FROM driver_documents d WHERE id=$1 AND driver_id=$2::UUID AND id=(SELECT id FROM driver_documents WHERE driver_id=$2::UUID AND type=d.type ORDER BY submitted_at DESC,id DESC LIMIT 1)`, id, driverID(c)).Scan(&status, &metadata, &key, &docType, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			driverError(c, 404, "document_not_found", "Document not found")
			return
		}
		if err != nil {
			driverError(c, 503, "document_unavailable", "Document unavailable")
			return
		}
		if status != "approved" || (expires != nil && !expires.After(time.Now())) {
			driverError(c, 409, "document_locked", "This document is not approved for viewing")
			return
		}
	}
	if !a.authenticateDocumentPIN(c, body.PIN, nil) {
		return
	}
	if kind == "identity" {
		profile, err := readIdentityProfile(c, a.pool, driverID(c))
		if err != nil {
			driverError(c, 503, "document_unavailable", "Identity unavailable")
			return
		}
		raw, _ := json.Marshal(profile)
		_ = json.Unmarshal(raw, &fields)
		rows, err := a.pool.Query(c.Request.Context(), `SELECT DISTINCT ON(type) type,storage_key,status FROM driver_documents WHERE driver_id=$1::UUID AND type IN ('driver_picture','selfie_with_id','id_document') ORDER BY type,submitted_at DESC,id DESC`, driverID(c))
		if err != nil {
			driverError(c, 503, "document_unavailable", "Identity photos unavailable")
			return
		}
		type photo struct{ kind, key, status string }
		photos := []photo{}
		for rows.Next() {
			var item photo
			if err = rows.Scan(&item.kind, &item.key, &item.status); err != nil {
				break
			}
			photos = append(photos, item)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			driverError(c, 503, "document_unavailable", "Identity photos unavailable")
			return
		}
		for _, item := range photos {
			if item.status == "approved" && !a.appendDocumentPreview(c, &files, item.key, item.kind, "") {
				return
			}
		}

	} else if kind == "inspection" {
		var partner, location, reason string
		var inspected *time.Time
		if err := a.pool.QueryRow(c.Request.Context(), `SELECT partner_name,location_name,outcome_reason,inspected_at FROM driver_inspections WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, driverID(c)).Scan(&partner, &location, &reason, &inspected); err != nil {
			driverError(c, 503, "document_unavailable", "Inspection unavailable")
			return
		}
		fields = gin.H{"Inspector": partner, "Location": location, "Outcome": reason, "Inspected at": inspected}
	} else {
		var data struct {
			Fields map[string]string      `json:"fields"`
			Files  []documentEvidenceFile `json:"files"`
		}
		if json.Unmarshal(metadata, &data) != nil {
			driverError(c, 503, "document_unavailable", "Document details unavailable")
			return
		}
		for k, v := range data.Fields {
			fields[k] = v
		}
		fields["Document type"] = docType
		fields["Expires"] = expires
		if len(data.Files) > 0 {
			for _, f := range data.Files {
				if !a.appendDocumentPreview(c, &files, f.StorageKey, f.Side, f.ContentType) {
					return
				}
			}
			key = ""
		}
	}
	if key != "" {
		if !a.appendDocumentPreview(c, &files, key, "document", "") {
			return
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"fields": fields, "files": files, "expiresInSeconds": 120}})
}
func (a *driverAPI) appendDocumentPreview(c *gin.Context, files *[]gin.H, key, side, mime string) bool {
	if a.files == nil {
		driverError(c, 503, "document_unavailable", "Document storage unavailable")
		return false
	}
	if mime == "" {
		info, err := a.files.Head(c.Request.Context(), key)
		if err != nil {
			driverError(c, 503, "document_unavailable", "Document unavailable")
			return false
		}
		mime = info.ContentType
	}
	url, err := a.files.PresignGet(c.Request.Context(), key, 2*time.Minute)
	if err != nil {
		driverError(c, 503, "document_unavailable", "Document preview unavailable")
		return false
	}
	*files = append(*files, gin.H{"side": side, "url": url, "contentType": mime})
	return true
}
