package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminMonnifyEncryptedSettingsAndCollectionGuard(t *testing.T) {
	dsn := os.Getenv("MONNIFY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable Monnify database required")
	}
	ctx := context.Background()
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = shareddb.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	staff := uuid.NewString()
	if _, e = p.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff); e != nil {
		t.Fatal(e)
	}
	api := &adminAPI{pool: p, settingsKey: []byte("01234567890123456789012345678901")}
	s, e := monnifysettings.Read(ctx, p, 0)
	if e != nil {
		t.Fatal(e)
	}
	call := func(h gin.HandlerFunc, method, body, actor, trip string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("admin", adminIdentity{ID: staff})
		c.Set("authenticated-user", gatewayauth.User{ID: actor, Roles: []string{"rider", "driver"}})
		c.Params = gin.Params{{Key: "tripID", Value: trip}}
		h(c)
		return w
	}
	b := map[string]any{"version": s.Version, "enabled": true, "tripPaymentsEnabled": true, "environment": "sandbox", "apiKey": "test-api-private", "secretKey": "test-secret-private", "contractCode": "test-contract-private"}
	raw, _ := json.Marshal(b)
	w := call(api.putMonnifySettings, "PUT", string(raw), staff, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("secret leaked")
	}
	saved, e := monnifysettings.Read(ctx, p, 0)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := monnifysettings.Open(api.settingsKey, saved.APIKey, "api-key")
	if e != nil || opened != "test-api-private" {
		t.Fatal("encrypted persistence", e)
	}
	if w = call(api.putMonnifySettings, "PUT", string(raw), staff, ""); w.Code != 409 {
		t.Fatal("stale update", w.Body.String())
	}
	b["version"] = saved.Version
	b["apiKey"] = ""
	b["secretKey"] = ""
	b["contractCode"] = ""
	raw, _ = json.Marshal(b)
	if w = call(api.putMonnifySettings, "PUT", string(raw), staff, ""); w.Code != 200 {
		t.Fatal("blank retention", w.Body.String())
	}
	saved, e = monnifysettings.Read(ctx, p, 0)
	if e != nil {
		t.Fatal(e)
	}
	opened, e = monnifysettings.Open(api.settingsKey, saved.APIKey, "api-key")
	if e != nil || opened != "test-api-private" {
		t.Fatal("blank replaced saved key", e)
	}
	b["version"] = saved.Version
	b["environment"] = "live"
	raw, _ = json.Marshal(b)
	if w = call(api.putMonnifySettings, "PUT", string(raw), staff, ""); w.Code != 400 {
		t.Fatal("live enabled during sandbox rollout", w.Body.String())
	}
	var audits int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE admin_id=$1::uuid AND action='monnify.settings.updated'`, staff).Scan(&audits); e != nil || audits != 2 {
		t.Fatal("audit", audits, e)
	}
	if adminRoutePermission("PUT", "/admin/settings/monnify") != permissionSettings || adminRoutePermission("GET", "/admin/finance/trip-collections") != permissionLedgerRead {
		t.Fatal("payment RBAC")
	}
	rider, driver, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e = p.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{rider, driver} {
		exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider','driver'])`, id)
	}
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed')`, trip, rider, driver, fare)
	exec(`INSERT INTO trip_settlements(trip_id,rider_id,driver_id,status,expected_amount_kobo,collection_method)VALUES($1::uuid,$2::uuid,$3::uuid,'pending',10000,'monnify')`, trip, rider, driver)
	exec(`INSERT INTO rider_trip_payments(trip_id,rider_id,driver_id,amount_kobo,fare_kobo,debt_kobo,payment_reference,monnify_config_version)VALUES($1::uuid,$2::uuid,$3::uuid,10000,10000,0,$4,$5)`, trip, rider, driver, "test-"+trip, saved.Version)
	w = call(api.monnifyCollections, "GET", "", staff, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "paymentReference") || strings.Contains(w.Body.String(), "private") {
		t.Fatal("Admin collections", w.Body.String())
	}
	verified := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			TripID  string `json:"tripId"`
			RiderID string `json:"riderId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if r.URL.Path != "/internal/trip-payments/refresh" || b.TripID != trip || b.RiderID != rider || r.Header.Get("X-Internal-Service-Token") != "test-internal" {
			w.WriteHeader(400)
			return
		}
		verified = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"status":"pending","providerVerified":false}}`))
	}))
	defer server.Close()
	t.Setenv("PAYMENT_SERVICE_URL", server.URL)
	t.Setenv("PAYMENT_SERVICE_AUDIENCE", "")
	t.Setenv("INTERNAL_SERVICE_TOKEN", "test-internal")
	w = call(api.verifyMonnifyCollection, "POST", "{}", staff, trip)
	if w.Code != 200 || !verified {
		t.Fatal("Admin provider verification", w.Body.String())
	}
	if e = p.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE admin_id=$1::uuid AND action='monnify.trip.verification.requested'`, staff).Scan(&audits); e != nil || audits != 1 {
		t.Fatal("verification audit", audits, e)
	}
	d := &driverAPI{pool: p}
	w = call(func(c *gin.Context) { d.mutateTripSettlement(c, "confirmed", "") }, "POST", "{}", driver, trip)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "provider_collection_in_progress") {
		t.Fatal("Driver confirmation must not race online payment", w.Body.String())
	}
	w = call(func(c *gin.Context) { riderPaymentStatus(c, p) }, "GET", "", driver, trip)
	if w.Code != 404 {
		t.Fatal("foreign payment", w.Body.String())
	}
}
