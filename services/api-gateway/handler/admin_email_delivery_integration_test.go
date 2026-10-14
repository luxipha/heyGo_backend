package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestAdminEmailDeliveryPersistenceAndWriteOnlySecret(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	staff := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, staff, "email-"+staff)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM admin_users WHERE id=$1::UUID`, staff)
	// Isolated integration DB: preserve any prior settings while testing this singleton.
	var backup []byte
	_ = pool.QueryRow(ctx, `SELECT row_to_json(s)::text FROM admin_email_delivery_settings s WHERE id=1`).Scan(&backup)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM admin_email_delivery_settings WHERE id=1`)
		if len(backup) > 0 {
			_, _ = pool.Exec(ctx, `INSERT INTO admin_email_delivery_settings SELECT * FROM json_populate_record(NULL::admin_email_delivery_settings,$1::json)`, string(backup))
		}
	}()
	_, _ = pool.Exec(ctx, `DELETE FROM admin_email_delivery_settings WHERE id=1`)
	api := &adminAPI{pool: pool, settingsKey: []byte("01234567890123456789012345678901")}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}); c.Next() })
	router.GET("/settings", api.getEmailDelivery)
	router.PUT("/settings", api.putEmailDelivery)
	request := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodGet, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"resendEnabled":true`) {
		t.Fatalf("defaults: %d %s", w.Code, w.Body.String())
	}
	body := `{"resendEnabled":true,"smtpEnabled":true,"primaryProvider":"resend","fallbackEnabled":true,"smtpHost":"smtp.example.test","smtpPort":587,"smtpTlsMode":"starttls","smtpUsername":"mailer","smtpPassword":"private-smtp-secret","smtpFromEmail":"otp@example.test","smtpFromName":"HeyGo"}`
	w = request(http.MethodPut, body)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-smtp-secret") || strings.Contains(w.Body.String(), "ciphertext") {
		t.Fatal("secret leaked")
	}
	s, err := readEmailDeliverySettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := openAdminValue(api.settingsKey, s.Ciphertext, "heygo-smtp-password-v1")
	if err != nil || secret != "private-smtp-secret" {
		t.Fatal("password not encrypted/persisted")
	}
	var safe map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &safe); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodPut, strings.Replace(body, `"smtpPassword":"private-smtp-secret",`, "", 1))
	if w.Code != 200 {
		t.Fatalf("blank preserve: %s", w.Body.String())
	}
	next, _ := readEmailDeliverySettings(ctx, pool)
	if string(next.Ciphertext) != string(s.Ciphertext) {
		t.Fatal("blank password replaced secret")
	}
	w = request(http.MethodPut, strings.Replace(body, `"starttls"`, `"none"`, 1))
	if w.Code != 400 {
		t.Fatal("plaintext TLS mode accepted")
	}
	if adminRoutePermission(http.MethodPut, "/admin/settings/email-delivery") != permissionSettings {
		t.Fatal("settings permissions missing")
	}
}
