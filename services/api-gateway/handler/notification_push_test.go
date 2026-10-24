package handler

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFCMSenderOAuthAndDelivery(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var apiStatus = http.StatusOK
	riderEnvelope := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if r.Form.Get("grant_type") != fcmTokenGrant || !strings.Contains(r.Form.Get("assertion"), ".") {
				t.Errorf("unexpected OAuth form")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"test-access-token","expires_in":3600}`))
		case "/v1/projects/test-project/messages:send":
			if r.Header.Get("Authorization") != "Bearer test-access-token" {
				t.Errorf("authorization=%q", r.Header.Get("Authorization"))
			}
			var payload struct {
				Message struct {
					Token        string `json:"token"`
					Notification struct {
						Title string `json:"title"`
						Body  string `json:"body"`
					} `json:"notification"`
					Data map[string]string `json:"data"`
				} `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			if (payload.Message.Token != "device-token" && payload.Message.Token != "gone-token") || payload.Message.Notification.Title != "Trip complete" || payload.Message.Data["notification_id"] == "" {
				t.Errorf("unexpected message: %+v", payload.Message)
			}
			if riderEnvelope && (payload.Message.Data["audience"] != "rider" || payload.Message.Data["schema_version"] != "1" || len(payload.Message.Data) != 3) {
				t.Errorf("Rider payload must contain only inbox identity and schema")
			}
			w.WriteHeader(apiStatus)
			if apiStatus == http.StatusNotFound {
				_, _ = w.Write([]byte(`{"error":{"details":[{"errorCode":"UNREGISTERED"}]}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	sender := &fcmSender{account: firebaseServiceAccount{ClientEmail: "sender@example.test", TokenURI: server.URL + "/token"}, projectID: "test-project", privateKey: key, tokenURL: server.URL + "/token", apiBaseURL: server.URL + "/v1", client: &http.Client{Timeout: 3 * time.Second}}
	if err := sender.send(context.Background(), "device-token", "Trip complete", "Your trip has completed.", "42", "trip"); err != nil {
		t.Fatalf("send: %v", err)
	}
	riderEnvelope = true
	if err := sender.sendWithData(context.Background(), "device-token", "Trip complete", "Open HeyGo to review the latest status.", map[string]string{"notification_id": "42", "audience": "rider", "schema_version": "1"}); err != nil {
		t.Fatal(err)
	}
	riderEnvelope = false
	apiStatus = http.StatusNotFound
	if err := sender.send(context.Background(), "gone-token", "Trip complete", "Your trip has completed.", "43", "trip"); err != errFCMUnregistered {
		t.Fatalf("unregistered token error=%v", err)
	}
}

func TestFCMADCDelivery(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/projects/firebase-project/messages:send" || r.Header.Get("Authorization") != "Bearer adc-test-token" {
			t.Error("ADC project/auth mismatch")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	sender := &fcmSender{projectID: "firebase-project", tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-test-token"}), apiBaseURL: server.URL, client: server.Client()}
	if err := sender.send(context.Background(), "test-token", "Title", "Body", "1", "trip"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("message not sent through ADC")
	}
	sender.tokenSource = failedFCMTokenSource{}
	if err := sender.send(context.Background(), "test-token", "Title", "Body", "1", "trip"); err == nil {
		t.Fatal("credential failure ignored")
	}
	if calls != 1 {
		t.Fatal("delivery attempted without valid credentials")
	}
}

type failedFCMTokenSource struct{}

func (failedFCMTokenSource) Token() (*oauth2.Token, error) {
	return nil, errors.New("token unavailable")
}
func TestFCMConfigurationModes(t *testing.T) {
	t.Setenv("FCM_SERVICE_ACCOUNT_FILE", "")
	t.Setenv("FCM_PROJECT_ID", "")
	t.Setenv("FCM_AUTH_MODE", "")
	if fcmConfigured() {
		t.Fatal("unconfigured sender enabled")
	}
	t.Setenv("FCM_PROJECT_ID", "firebase-project")
	if fcmConfigured() {
		t.Fatal("project alone implicitly enabled credentials")
	}
	t.Setenv("FCM_AUTH_MODE", "adc")
	if !fcmConfigured() {
		t.Fatal("explicit ADC configuration not recognized")
	}
	t.Setenv("FCM_PROJECT_ID", "")
	if _, err := newFCMSenderFromEnv(); err == nil {
		t.Fatal("ADC allowed without explicit target project")
	}
	t.Setenv("FCM_AUTH_MODE", "")
	t.Setenv("FCM_SERVICE_ACCOUNT_FILE", "/not-a-real-service-account.json")
	if !fcmConfigured() {
		t.Fatal("legacy file mode lost")
	}
	if _, err := newFCMSenderFromEnv(); err == nil {
		t.Fatal("missing file accepted")
	}
}
