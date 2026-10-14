package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminSMSPersistenceAndPublicAvailability(t *testing.T) {
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
	_, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, staff, "sms-"+staff)
	if err != nil {
		t.Fatal(err)
	}
	var backup []byte
	_ = pool.QueryRow(ctx, `SELECT row_to_json(s)::text FROM admin_sms_settings s WHERE id=1`).Scan(&backup)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM admin_sms_settings WHERE id=1`)
		if len(backup) > 0 {
			_, _ = pool.Exec(ctx, `INSERT INTO admin_sms_settings SELECT * FROM json_populate_record(NULL::admin_sms_settings,$1::json)`, string(backup))
		}
	}()
	_, _ = pool.Exec(ctx, `DELETE FROM admin_sms_settings WHERE id=1`)
	key := []byte("01234567890123456789012345678901")
	api := &adminAPI{pool: pool, settingsKey: key}
	auth := &phoneAuthAPI{pool: pool, pepper: key, deliveryKey: key, smsSender: &configuredKudiSMSSender{pool: pool, key: key}, emailSender: &configuredResendOTPSender{pool: pool, key: key}}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}); c.Next() })
	router.PUT("/sms", api.putSMSSettings)
	router.GET("/sms", api.getSMSSettings)
	router.GET("/auth/channels", auth.authChannels)
	router.POST("/phone", auth.requestOTP)
	req := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	if ready, err := authSMSReady(ctx, pool, key); err != nil || ready {
		t.Fatal("unconfigured SMS enabled")
	}
	w := req("PUT", "/sms", `{"enabled":true}`)
	if w.Code != 400 {
		t.Fatal("unconfigured enable accepted")
	}
	body := `{"enabled":true,"apiKey":"synthetic-kudi-key","senderId":"HeyGo","appNameCode":"app","templateCode":"template"}`
	w = req("PUT", "/sms", body)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "synthetic-kudi-key") {
		t.Fatal("secret leaked")
	}
	saved, _ := readSMSSettings(ctx, pool)
	secret, err := openAdminValue(key, saved.Ciphertext, "heygo-kudi-api-key-v1")
	if err != nil || secret != "synthetic-kudi-key" {
		t.Fatal("encrypted persistence failed")
	}
	w = req("GET", "/auth/channels", "")
	var envelope struct {
		Data struct {
			SMS struct {
				Enabled bool `json:"enabled"`
			} `json:"sms"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || !envelope.Data.SMS.Enabled {
		t.Fatal("configured SMS unavailable")
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "appNameCode") || strings.Contains(w.Body.String(), "senderId") {
		t.Fatal("public secret/config leakage")
	}
	disabled := strings.Replace(strings.Replace(body, `"enabled":true`, `"enabled":false`, 1), `"apiKey":"synthetic-kudi-key",`, "", 1)
	w = req("PUT", "/sms", disabled)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	next, _ := readSMSSettings(ctx, pool)
	if string(next.Ciphertext) != string(saved.Ciphertext) {
		t.Fatal("blank key replaced credential")
	}
	w = req("POST", "/phone", `{"phone":"+2348012345678"}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "sms_unavailable") {
		t.Fatal("disabled SMS accepted new challenge")
	}
	if adminRoutePermission("PUT", "/admin/settings/sms") != permissionSettings {
		t.Fatal("missing SMS permission")
	}
}
