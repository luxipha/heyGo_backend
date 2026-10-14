// This command exercises real HTTP/session/staff handoffs on the named local
// seeded database. OTP delivery is intercepted in memory; it is not carrier proof.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/api-gateway/handler"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type recorder struct {
	mu   sync.Mutex
	code string
}

func (s *recorder) SendOTP(_ context.Context, _ string, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	target := os.Getenv("RIDER_STAGING_DATABASE_URL")
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || (u.Port() != "55439" && u.Port() != "55440") || u.Path != "/heygo_rider_staging" {
		return fmt.Errorf("only the named loopback seeded Rider staging database is permitted")
	}
	raw, err := os.ReadFile(".dev/rider-staging.env")
	if err != nil {
		return err
	}
	password := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "RIDER_STAGING_STAFF_PASSWORD="); ok {
			password = value
		}
	}
	if len(password) < 24 {
		return fmt.Errorf("private staging staff password is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: target, MaxConns: 4})
	if err != nil {
		return err
	}
	defer pool.Close()
	const rider = "a1100000-0000-4000-8000-000000000001"
	const caseID = "a1100000-0000-4000-8000-000000000031"
	const phone = "+2348012345678"
	var safe bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::UUID AND casper_id_user_id='heygo-admin-staging:rider' AND roles @> ARRAY['rider']) AND NOT EXISTS(SELECT 1 FROM users WHERE casper_id_user_id IS NULL OR casper_id_user_id NOT LIKE 'heygo-admin-staging:%')`, rider).Scan(&safe); err != nil || !safe {
		return fmt.Errorf("staging fixture guard failed")
	}
	if err = shareddb.Migrate(ctx, pool); err != nil {
		return err
	}
	// Bind only the known synthetic fixture to the documented intercepted phone.
	if _, err = pool.Exec(ctx, `UPDATE users SET phone_e164=$2 WHERE id=$1::UUID AND casper_id_user_id='heygo-admin-staging:rider'`, rider, phone); err != nil {
		return err
	}
	os.Setenv("LOG_ROTATE", "false")
	os.Setenv("ADMIN_COOKIE_SECURE", "false")
	if _, err = logs.Init("rider-seeded-staging-check"); err != nil {
		return err
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		return err
	}
	sender := &recorder{}
	users := gatewayauth.NewPostgresUserStore(pool)
	router := handler.NewHTTPHandler(nil, messaging.NewConnectionManager("http://localhost:3000"), gatewayauth.NewSessionMiddleware(pool, users), users, pool, nil, nil, handler.PhoneAuthConfig{OTPPepper: pepper, Sender: sender, EmailSender: sender}, pool.Ping)
	server := httptest.NewServer(router)
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	access, csrf := "", ""
	call := func(method, path string, body any, expected int) (map[string]any, error) {
		encoded, _ := json.Marshal(body)
		req, e := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(encoded))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer response.Body.Close()
		payload, e := io.ReadAll(response.Body)
		if e != nil {
			return nil, e
		}
		if response.StatusCode != expected {
			return nil, fmt.Errorf("%s %s returned HTTP %d; expected %d", method, path, response.StatusCode, expected)
		}
		if expected == 204 {
			return nil, nil
		}
		var decoded map[string]any
		if e = json.Unmarshal(payload, &decoded); e != nil {
			return nil, e
		}
		data, _ := decoded["data"].(map[string]any)
		return data, nil
	}
	challenge, err := call("POST", "/auth/phone/otp/request", map[string]string{"phone": phone}, 202)
	if err != nil {
		return err
	}
	sender.mu.Lock()
	code := sender.code
	sender.mu.Unlock()
	auth, err := call("POST", "/auth/phone/otp/verify", map[string]any{"phone": phone, "challengeId": challenge["challengeId"], "code": code, "deviceName": "controlled local Rider staging"}, 200)
	if err != nil {
		return err
	}
	access, _ = auth["accessToken"].(string)
	refresh, _ := auth["refreshToken"].(string)
	if access == "" || refresh == "" {
		return fmt.Errorf("real session issuance failed")
	}
	defer func() { _, _ = call("POST", "/auth/logout", nil, 200) }()
	if _, err = call("PATCH", "/rider/profile", map[string]string{"displayName": "Staging Rider"}, 200); err != nil {
		return err
	}
	const email = "rider.identity.staging@example.test"
	emailChallenge, err := call("POST", "/rider/identity/email-link/request", map[string]string{"destination": email}, 202)
	if err != nil {
		return err
	}
	sender.mu.Lock()
	emailCode := sender.code
	sender.mu.Unlock()
	if _, err = call("POST", "/rider/identity/email-link/confirm", map[string]any{"challengeId": emailChallenge["challengeId"], "newCode": emailCode}, 200); err != nil {
		return err
	}
	identity, err := call("GET", "/auth/me", nil, 200)
	if err != nil || identity["name"] != "Staging Rider" || identity["emailVerified"] != true {
		return fmt.Errorf("seeded Rider identity recovery failed")
	}
	for _, path := range []string{"/auth/me", "/rider/profile", "/rider/no-show-debts", "/rider/no-show-debt-payments", "/rider/trips/active", "/rider/trips", "/rider/profile/metrics", "/rider/notification-preferences", "/rider/notifications", "/support/cases/" + caseID} {
		if _, err = call("GET", path, nil, 200); err != nil {
			return err
		}
	}
	if _, err = call("PATCH", "/rider/notification-preferences", map[string]bool{"rideUpdates": true, "accountAlerts": true, "promotions": false}, 200); err != nil {
		return err
	}
	device := uuid.NewString()
	if _, err = call("POST", "/rider/devices", map[string]string{"id": device, "platform": "android", "pushToken": "controlled-not-a-provider-token-" + device}, 200); err != nil {
		return err
	}
	defer func() { _, _ = call("DELETE", "/rider/devices/"+device, nil, 204) }()
	staff, err := call("POST", "/admin/auth/login", map[string]string{"username": "rider-staging-support", "password": password}, 200)
	if err != nil {
		return err
	}
	csrf, _ = staff["csrfToken"].(string)
	defer func() { _, _ = call("POST", "/admin/auth/logout", nil, 200) }()
	if _, err = call("GET", "/admin/support/cases/"+caseID, nil, 200); err != nil {
		return err
	}
	if _, err = call("POST", "/admin/support/cases/"+caseID+"/messages", map[string]string{"body": "Controlled staging acknowledgment. This is not responder dispatch.", "clientMessageId": uuid.NewString()}, 201); err != nil {
		return err
	}
	// Refresh rotates the actual session before persisted state recovery reads.
	rotated, err := call("POST", "/auth/refresh", map[string]string{"refreshToken": refresh}, 200)
	if err != nil {
		return err
	}
	access, _ = rotated["accessToken"].(string)
	if _, err = call("GET", "/support/cases/"+caseID+"/messages", nil, 200); err != nil {
		return err
	}
	inbox, err := call("GET", "/rider/notifications", nil, 200)
	if err != nil {
		return err
	}
	rows, _ := inbox["items"].([]any)
	if len(rows) == 0 {
		return fmt.Errorf("staff reply did not persist a Rider inbox item")
	}
	item := rows[0].(map[string]any)
	id, _ := item["id"].(string)
	if _, err = call("GET", "/rider/notifications/"+id, nil, 200); err != nil {
		return err
	}
	if _, err = call("PATCH", "/rider/notifications/"+id, nil, 200); err != nil {
		return err
	}
	// Replay uses real session middleware and the durable event stream, with no URL credential logging.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/riders?afterEventId=evt_0"
	headers := http.Header{"Authorization": []string{"Bearer " + access}, "Origin": []string{"http://localhost:3000"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		return fmt.Errorf("authenticated Rider stream: %w", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	found := false
	for i := 0; i < 50; i++ {
		_, payload, e := conn.ReadMessage()
		if e != nil {
			return fmt.Errorf("Rider replay: %w", e)
		}
		var event map[string]any
		if json.Unmarshal(payload, &event) == nil && event["type"] == "rider.notification.created" {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("Rider inbox stream handoff was not observed")
	}
	fmt.Println("PASS: seeded local HTTP OTP interception/session/refresh, Rider-owned display name and intercepted verified-email linking, debt/payment recovery reads, Rider preferences/device, authenticated staff reply, persisted inbox/read and Rider WebSocket replay. No real SMS/email/push/provider or responder delivery was performed.")
	return nil
}
