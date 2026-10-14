package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

// Uses the actual gateway router/session middleware and PostgreSQL. Delivery
// adapters record test OTPs in memory; this does not prove carrier/Resend delivery.
func TestRiderAuthEnrollmentRotationAndRevocation(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	t.Setenv("LOG_ROTATE", "false")
	if _, err := logs.Init("rider-auth-integration-test"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
	gin.SetMode(gin.TestMode)
	for _, channel := range []string{"phone", "email"} {
		t.Run(channel, func(t *testing.T) {
			destination := "+2348012345678" // Synthetic Nigerian-format input; intercepted in memory, never sent as SMS.
			if channel == "email" {
				destination = "rider-auth-" + uuid.NewString() + "@example.com"
			}
			sender := &recordingEmailOTPSender{}
			users := gatewayauth.NewPostgresUserStore(pool)
			router := NewHTTPHandler(nil, nil, gatewayauth.NewSessionMiddleware(pool, users), users, pool, nil, nil,
				PhoneAuthConfig{OTPPepper: []byte("01234567890123456789012345678901"), Sender: sender, EmailSender: sender}, func(context.Context) error { return nil })
			server := httptest.NewServer(router)
			defer server.Close()
			client := server.Client()
			call := func(method, path, token string, body any) (int, map[string]any) {
				t.Helper()
				raw, marshalErr := json.Marshal(body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				req, requestErr := http.NewRequest(method, server.URL+path, strings.NewReader(string(raw)))
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				response, requestErr := client.Do(req)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				defer response.Body.Close()
				encoded, readErr := io.ReadAll(response.Body)
				if readErr != nil {
					t.Fatal(readErr)
				}
				result := map[string]any{}
				if err := json.Unmarshal(encoded, &result); err != nil {
					t.Fatalf("%s returned invalid JSON", path)
				}
				return response.StatusCode, result
			}
			requireStatus := func(want, got int, step string) {
				t.Helper()
				if got != want {
					t.Fatalf("%s status=%d want=%d", step, got, want)
				}
			}
			var userID string
			defer func() {
				cleanup := context.Background()
				if userID != "" {
					_, _ = pool.Exec(cleanup, `DELETE FROM api_idempotency_keys WHERE actor_id=$1::UUID`, userID)
					_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1::UUID`, userID)
				}
				_, _ = pool.Exec(cleanup, `DELETE FROM auth_otp_challenges WHERE channel=$1 AND destination=$2`, channel, destination)
			}()
			for login := 0; login < 2; login++ {
				status, requested := call(http.MethodPost, "/auth/"+channel+"/otp/request", "", map[string]string{channel: destination})
				requireStatus(http.StatusAccepted, status, "OTP request")
				challenge := requested["data"].(map[string]any)["challengeId"].(string)
				deliveredTo, code := sender.sent()
				if deliveredTo != destination || len(code) != 6 {
					t.Fatal("OTP adapter delivery mismatch")
				}
				verification := map[string]string{channel: destination, "challengeId": challenge, "code": code, "deviceName": "Rider J-01/J-02 integration test"}
				status, verified := call(http.MethodPost, "/auth/"+channel+"/otp/verify", "", verification)
				requireStatus(http.StatusOK, status, "OTP verify")
				data := verified["data"].(map[string]any)
				access, refresh := data["accessToken"].(string), data["refreshToken"].(string)
				status, me := call(http.MethodGet, "/auth/me", access, nil)
				requireStatus(http.StatusOK, status, "session identity")
				identity := me["data"].(map[string]any)
				userID = identity["id"].(string)
				if login == 0 {
					if len(identity["roles"].([]any)) != 0 {
						t.Fatal("first-time account unexpectedly enrolled")
					}
					status, _ = call(http.MethodGet, "/support/topics?kind=support", access, nil)
					requireStatus(http.StatusForbidden, status, "unenrolled Rider gate")
					status, _ = call(http.MethodGet, "/rider/trips/active", access, nil)
					requireStatus(http.StatusForbidden, status, "unenrolled Rider recovery gate")
					for attempt := 0; attempt < 2; attempt++ {
						status, _ = call(http.MethodPost, "/auth/register", access, map[string]string{"role": "rider"})
						requireStatus(http.StatusOK, status, "idempotent Rider enrollment")
					}
				}
				status, me = call(http.MethodGet, "/auth/me", access, nil)
				requireStatus(http.StatusOK, status, "enrolled identity")
				roles := me["data"].(map[string]any)["roles"].([]any)
				if len(roles) != 1 || roles[0] != "rider" {
					t.Fatal("Rider role was not persisted exactly once")
				}
				status, _ = call(http.MethodGet, "/support/topics?kind=support", access, nil)
				requireStatus(http.StatusOK, status, "Rider-protected access")
				status, _ = call(http.MethodGet, "/rider/trips/active", access, nil)
				requireStatus(http.StatusOK, status, "enrolled Rider recovery")
				status, _ = call(http.MethodPost, "/auth/"+channel+"/otp/verify", "", verification)
				if status == http.StatusOK {
					t.Fatal("consumed OTP was accepted")
				}
				status, rotated := call(http.MethodPost, "/auth/refresh", "", map[string]string{"refreshToken": refresh})
				requireStatus(http.StatusOK, status, "refresh rotation")
				rotatedData := rotated["data"].(map[string]any)
				nextAccess, nextRefresh := rotatedData["accessToken"].(string), rotatedData["refreshToken"].(string)
				if nextAccess == access || nextRefresh == refresh {
					t.Fatal("tokens did not rotate")
				}
				status, _ = call(http.MethodGet, "/auth/me", access, nil)
				requireStatus(http.StatusUnauthorized, status, "old access invalidation")
				status, _ = call(http.MethodPost, "/auth/refresh", "", map[string]string{"refreshToken": refresh})
				requireStatus(http.StatusUnauthorized, status, "old refresh replay rejection")
				status, _ = call(http.MethodGet, "/auth/me", nextAccess, nil)
				requireStatus(http.StatusOK, status, "rotated access")
				status, _ = call(http.MethodPost, "/auth/logout", nextAccess, nil)
				requireStatus(http.StatusOK, status, "backend logout")
				status, _ = call(http.MethodGet, "/auth/me", nextAccess, nil)
				requireStatus(http.StatusUnauthorized, status, "revoked access")
				status, _ = call(http.MethodPost, "/auth/refresh", "", map[string]string{"refreshToken": nextRefresh})
				requireStatus(http.StatusUnauthorized, status, "revoked refresh")
			}
			t.Logf("verified synthetic %s identity %s through configured loopback gateway %s", channel, destination, server.URL)
		})
	}
}

// A failed database read must not masquerade as invalid credentials.
func TestRefreshDatabaseFailureIsTemporary(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	// Force a database query error without changing data: temporarily hide the
	// sessions relation for this connection using its search_path.
	_, err = blocker.Exec(ctx, `SET search_path=pg_catalog`)
	if err != nil {
		t.Fatal(err)
	}
	blocker.Release()
	api := &phoneAuthAPI{pool: pool}
	router := gin.New()
	router.POST("/refresh", api.refresh)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(fmt.Sprintf(`{"refreshToken":%q}`, strings.Repeat("r", 48)))))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("database read failure status=%d want=503", response.Code)
	}
}
