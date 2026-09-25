package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	sharedauth "github.com/cprakhar/uber-clone/shared/auth"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/cprakhar/uber-clone/shared/storage"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type privacyMemoryStore struct {
	mu           sync.Mutex
	objects      map[string][]byte
	presignCount int
	presignTTL   time.Duration
}

func newPrivacyMemoryStore() *privacyMemoryStore {
	return &privacyMemoryStore{objects: map[string][]byte{}}
}
func (s *privacyMemoryStore) PresignPut(context.Context, string, string, int64, time.Duration) (string, http.Header, error) {
	return "", nil, nil
}
func (s *privacyMemoryStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presignCount++
	s.presignTTL = ttl
	return fmt.Sprintf("https://private.example/%s?serial=%d", key, s.presignCount), nil
}
func (s *privacyMemoryStore) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}
func (s *privacyMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}
func (s *privacyMemoryStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (s *privacyMemoryStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
	value, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = value
	return nil
}

func TestDriverDataExportRequiresVerifiedEmailAndQueuesOneRequest(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driverID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "export-subject:"+driverID, "export-human:"+driverID); err != nil {
		t.Fatal(err)
	}
	adminID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, adminID, "export-admin-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	encryptionKey := bytes.Repeat([]byte{9}, 32)
	apiKeyCiphertext, err := sealAdminSecret(encryptionKey, "re_test_key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_resend_settings(id,api_key_ciphertext,from_email,from_name,updated_by) VALUES(1,$1,'noreply@example.com','HeyGo',$2::UUID)
		ON CONFLICT(id) DO UPDATE SET api_key_ciphertext=EXCLUDED.api_key_ciphertext,from_email=EXCLUDED.from_email,from_name=EXCLUDED.from_name,updated_by=EXCLUDED.updated_by`, apiKeyCiphertext, adminID); err != nil {
		t.Fatal(err)
	}
	api := &driverAPI{pool: pool, files: newPrivacyMemoryStore()}
	router := gin.New()
	router.POST("/driver/data-export", func(ctx *gin.Context) {
		ctx.Set("authenticated-user", gatewayauth.User{ID: driverID, Email: "driver@example.com", EmailVerified: ctx.GetHeader("X-Email-Verified") == "true"})
		api.requestDriverDataExport(ctx)
	})
	unverified := httptest.NewRecorder()
	router.ServeHTTP(unverified, httptest.NewRequest(http.MethodPost, "/driver/data-export", nil))
	if unverified.Code != http.StatusConflict {
		t.Fatalf("unverified email was accepted: %d %s", unverified.Code, unverified.Body.String())
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/driver/data-export", nil)
		req.Header.Set("X-Email-Verified", "true")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("verified export request failed: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	var count int
	var email string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),MIN(requested_email) FROM driver_data_exports WHERE driver_id=$1::UUID AND status IN ('pending','processing')`, driverID).Scan(&count, &email); err != nil {
		t.Fatal(err)
	}
	if count != 1 || email != "driver@example.com" {
		t.Fatalf("expected one queued export to the verified address, count=%d email=%q", count, email)
	}
	if _, err := pool.Exec(ctx, `UPDATE driver_data_exports SET status='cancelled',requested_email='' WHERE status IN ('pending','processing') AND driver_id<>$1::UUID`, driverID); err != nil {
		t.Fatal(err)
	}
	var firstBody, secondBody string
	var sends int
	send := func(_ context.Context, idempotencyKey, apiKey, fromEmail, fromName, to, subject, textBody, htmlBody string) error {
		sends++
		if idempotencyKey == "" || apiKey != "re_test_key" || fromEmail != "noreply@example.com" || fromName != "HeyGo" || to != "driver@example.com" || subject != "Your HeyGo data archive" || htmlBody == "" {
			t.Fatalf("unexpected export email contract: key=%q from=%q to=%q subject=%q", idempotencyKey, fromEmail, to, subject)
		}
		if sends == 1 {
			firstBody = textBody
			return errors.New("temporary provider failure")
		}
		secondBody = textBody
		return nil
	}
	if !processNextDriverDataExportUsingSender(ctx, pool, api.files, encryptionKey, send) {
		t.Fatal("first export delivery was not processed")
	}
	if _, err := pool.Exec(ctx, `UPDATE driver_data_exports SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE driver_id=$1::UUID AND status='pending'`, driverID); err != nil {
		t.Fatal(err)
	}
	if !processNextDriverDataExportUsingSender(ctx, pool, api.files, encryptionKey, send) {
		t.Fatal("retried export delivery was not processed")
	}
	var status string
	var sentAt, expiresAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status,sent_at,expires_at FROM driver_data_exports WHERE driver_id=$1::UUID`, driverID).Scan(&status, &sentAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || sentAt == nil || expiresAt == nil || !expiresAt.After(sentAt.Add(6*24*time.Hour)) || firstBody == "" || firstBody != secondBody {
		t.Fatalf("export retry did not send one stable seven-day link: status=%s sentAt=%v expiresAt=%v bodiesEqual=%v", status, sentAt, expiresAt, firstBody == secondBody)
	}
	api.files.(*privacyMemoryStore).mu.Lock()
	presignCount, presignTTL := api.files.(*privacyMemoryStore).presignCount, api.files.(*privacyMemoryStore).presignTTL
	api.files.(*privacyMemoryStore).mu.Unlock()
	if sends != 2 || presignCount != 1 || presignTTL != driverExportLifetime {
		t.Fatalf("unexpected retry/expiry behavior: sends=%d signs=%d ttl=%s", sends, presignCount, presignTTL)
	}
}

func TestDriverDeletionWaitsForStaffAndPreservesTripFinancialRecords(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driverID, riderID, adminID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	casperSubject, humanID := "privacy-subject:"+driverID, "privacy-human:"+driverID
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,nin_verified) VALUES($1::UUID,$2,$3,ARRAY['driver'],TRUE,TRUE)`, driverID, casperSubject, humanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, riderID, "privacy-rider:"+riderID, "privacy-rider-human:"+riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, adminID, "privacy-admin-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name,photo_url) VALUES($1::UUID,'Sensitive Driver Name','https://private.example/photo')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_vehicles(driver_id,package_slug,make,model,model_year,color,plate) VALUES($1::UUID,'sedan','Toyota','Camry',2022,'black','ABC123')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,profile_pic,car_plate,package_slug,status,available,online_requested) VALUES($1::UUID,'Sensitive Driver Name','https://private.example/photo','ABC123','sedan','available',TRUE,TRUE)`, driverID); err != nil {
		t.Fatal(err)
	}
	fileKey := "driver-documents/" + driverID + "/license.pdf"
	if _, err := pool.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key) VALUES($1::UUID,$2::UUID,'driver_license','approved',$3)`, uuid.NewString(), driverID, fileKey); err != nil {
		t.Fatal(err)
	}
	fareID, tripID := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{"distanceMeters":1200}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,completed_at) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed',NOW())`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}
	archiveStore := newPrivacyMemoryStore()
	archiveStore.objects[fileKey] = []byte("private driver document")
	exportID := uuid.NewString()
	archiveKey, err := buildDriverDataArchive(ctx, pool, archiveStore, driverID, exportID)
	if err != nil {
		t.Fatalf("build private Driver archive: %v", err)
	}
	archiveBytes := archiveStore.objects[archiveKey]
	archiveReader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("open generated Driver archive: %v", err)
	}
	var hasManifest, hasDocument bool
	for _, entry := range archiveReader.File {
		switch entry.Name {
		case "heygo-driver-export.json":
			hasManifest = true
			opened, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			manifest, readErr := io.ReadAll(opened)
			_ = opened.Close()
			if readErr != nil || !json.Valid(manifest) || !bytes.Contains(manifest, []byte(tripID)) {
				t.Fatalf("archive manifest omitted trip data or was invalid: readErr=%v", readErr)
			}
		}
		if strings.HasPrefix(entry.Name, "documents/driver_license-") {
			hasDocument = true
		}
	}
	if !hasManifest || !hasDocument {
		t.Fatalf("archive missing required entries: manifest=%v document=%v files=%v", hasManifest, hasDocument, archiveReader.File)
	}
	requestID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO driver_account_deletion_requests(id,driver_id,casper_subject_hash,human_id_hash) VALUES($1::UUID,$2::UUID,$3,$4)`, requestID, driverID, gatewayauth.IdentityHash(casperSubject), gatewayauth.IdentityHash(humanID)); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	api := &adminAPI{pool: pool}
	router.POST("/review/:requestID", func(ctx *gin.Context) {
		ctx.Set("admin", adminIdentity{ID: adminID, Username: "reviewer"})
		api.reviewAccountDeletionRequest(ctx)
	})
	request := httptest.NewRequest(http.MethodPost, "/review/"+requestID, strings.NewReader(`{"decision":"approve"}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("approval failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var active bool
	var anonymizedHuman string
	if err := pool.QueryRow(ctx, `SELECT active,human_id FROM users WHERE id=$1::UUID`, driverID).Scan(&active, &anonymizedHuman); err != nil {
		t.Fatal(err)
	}
	if active || anonymizedHuman == humanID {
		t.Fatalf("account was not deactivated and anonymized: active=%v humanId=%q", active, anonymizedHuman)
	}
	var name, plate, status string
	if err := pool.QueryRow(ctx, `SELECT p.display_name,v.plate,d.status FROM driver_profiles p JOIN driver_vehicles v USING(driver_id) JOIN drivers d ON d.id=p.driver_id WHERE p.driver_id=$1::UUID`, driverID).Scan(&name, &plate, &status); err != nil {
		t.Fatal(err)
	}
	if name != "Deleted Driver" || plate != "" || status != "offline" {
		t.Fatalf("personal Driver profile was not scrubbed: name=%q plate=%q status=%q", name, plate, status)
	}
	var tripStatus string
	var fare float64
	if err := pool.QueryRow(ctx, `SELECT t.status,f.total_fare_minor FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::UUID`, tripID).Scan(&tripStatus, &fare); err != nil {
		t.Fatal(err)
	}
	if tripStatus != "completed" || fare != 500000 {
		t.Fatalf("trip/financial history changed during account deletion: status=%s fare=%f", tripStatus, fare)
	}
	var queuedFiles int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_account_deletion_files WHERE request_id=$1::UUID AND storage_key=$2 AND deleted_at IS NULL`, requestID, fileKey).Scan(&queuedFiles); err != nil || queuedFiles != 1 {
		t.Fatalf("private document not queued for storage removal: count=%d err=%v", queuedFiles, err)
	}
	files := newPrivacyMemoryStore()
	if !processDriverDeletionFile(ctx, pool, files) {
		t.Fatal("queued private-file removal was not processed")
	}
	var completedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM driver_account_deletion_requests WHERE id=$1::UUID`, requestID).Scan(&completedAt); err != nil || completedAt == nil {
		t.Fatalf("request should complete after private file removal: completedAt=%v err=%v", completedAt, err)
	}
	store := gatewayauth.NewPostgresUserStore(pool)
	_, err = store.UpsertIdentity(ctx, sharedauth.Identity{Subject: casperSubject, HumanID: humanID})
	if !errors.Is(err, gatewayauth.ErrAccountDeactivated) {
		t.Fatalf("deleted identity should be rejected, got %v", err)
	}
}
