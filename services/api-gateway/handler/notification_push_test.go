package handler

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
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
	apiStatus = http.StatusNotFound
	if err := sender.send(context.Background(), "gone-token", "Trip complete", "Your trip has completed.", "43", "trip"); err != errFCMUnregistered {
		t.Fatalf("unregistered token error=%v", err)
	}
}
