package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDriverSessionActivityContactAndNotificationOwnership(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
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
	owner, foreign, rider := uuid.NewString(), uuid.NewString(), uuid.NewString()
	current, other, foreignSession := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{owner, foreign, rider} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['driver','rider'])`, id)
	}
	for _, s := range []struct{ id, owner, token string }{{current, owner, "owner-current"}, {other, owner, "owner-other"}, {foreignSession, foreign, "foreign-current"}} {
		exec(`INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name) VALUES($1::uuid,$2::uuid,$3,$4,NOW()+INTERVAL '1 hour',NOW()+INTERVAL '1 day','Actual phone')`, s.id, s.owner, gatewayauth.TokenHash(s.token), gatewayauth.TokenHash(s.token+"refresh"))
	}
	device := uuid.NewString()
	exec(`INSERT INTO driver_devices(id,driver_id,platform,push_token,session_id) VALUES($1::uuid,$2::uuid,'ios',$1,$3::uuid)`, device, owner, other)
	exec(`INSERT INTO driver_socket_sessions(driver_id,session_id,expires_at,auth_session_id) VALUES($1::uuid,$2::uuid,NOW()+INTERVAL '30 seconds',$3::uuid)`, owner, uuid.NewString(), other)

	call := func(who, token, method, path string, want int, bodies ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: who})
		g := r.Group("/", auth.Authenticate)
		registerPhoneSessionRoutes(g, pool)
		registerDriverRoutes(g, pool, nil)
		registerDeviceRoutes(g, pool)
		body := "{}"
		if len(bodies) > 0 {
			body = bodies[0]
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s=%d want %d %s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	currentDevice := uuid.NewString()
	call(owner, "owner-current", "POST", "/devices", 200, `{"id":"`+currentDevice+`","platform":"ios","pushToken":"session-`+currentDevice+`","appVersion":"1"}`)
	var bound string
	if err = pool.QueryRow(ctx, `SELECT session_id::text FROM driver_devices WHERE id=$1::uuid`, currentDevice).Scan(&bound); err != nil || bound != current {
		t.Fatal("Device was not linked to authenticated session")
	}
	listed := call(owner, "owner-current", "GET", "/auth/sessions", 200)
	var data struct {
		Data struct {
			Items []struct {
				ID      string `json:"id"`
				Current bool   `json:"current"`
			}
		} `json:"data"`
	}
	if err = json.Unmarshal(listed.Body.Bytes(), &data); err != nil || len(data.Data.Items) != 2 {
		t.Fatal("Incorrect session list")
	}
	foundCurrent := false
	for _, v := range data.Data.Items {
		if v.ID == foreignSession {
			t.Fatal("Foreign session leaked")
		}
		if v.ID == current && v.Current {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Fatal("Current session not identified")
	}
	call(owner, "owner-current", "DELETE", "/auth/sessions/"+foreignSession, 404)
	call(owner, "owner-current", "DELETE", "/auth/sessions/"+other, 200)
	var devices int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_devices WHERE id=$1::uuid`, device).Scan(&devices); err != nil || devices != 0 {
		t.Fatal("Revoked session kept push device")
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_socket_sessions WHERE driver_id=$1::uuid`, owner).Scan(&devices); err != nil || devices != 0 {
		t.Fatal("Revoked session kept socket lease")
	}
	call(owner, "owner-current", "POST", "/auth/sessions/revoke-others", 200)
	var live bool
	if err = pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM user_sessions WHERE id=$1::uuid`, current).Scan(&live); err != nil || !live {
		t.Fatal("Revoke others revoked current")
	}
	var notification string
	if err = pool.QueryRow(ctx, `INSERT INTO driver_notifications(driver_id,source_event_id,type,title,message) VALUES($1::uuid,$2,'announcement','Verified title','Verified body') RETURNING id::text`, owner, uuid.NewString()).Scan(&notification); err != nil {
		t.Fatal(err)
	}
	call(owner, "owner-current", "GET", "/driver/notifications/"+notification, 200)
	call(foreign, "foreign-current", "GET", "/driver/notifications/"+notification, 404)
	fare, trip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::uuid,'Driver','TEST','sedan','on_trip')`, owner)
	exec(`UPDATE users SET phone_e164='+2348123456789' WHERE id=$1::uuid`, rider)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,origin_region_code) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'accepted','TEST')`, trip, rider, fare, owner)
	contact := call(owner, "owner-current", "GET", "/driver/trips/"+trip+"/contact", 200)
	if !strings.Contains(contact.Body.String(), "+2348123456789") {
		t.Fatal("Registered phone not returned")
	}
	call(foreign, "foreign-current", "GET", "/driver/trips/"+trip+"/contact", 404)
	exec(`UPDATE trips SET status='completed' WHERE id=$1::uuid`, trip)
	call(owner, "owner-current", "GET", "/driver/trips/"+trip+"/contact", 404)
}
