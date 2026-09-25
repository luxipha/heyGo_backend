package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminResendSecretEncryptionAndDelivery(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := sealAdminSecret(key, "re_secret-never-return-this")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := openAdminSecret(key, ciphertext)
	if err != nil || plaintext != "re_secret-never-return-this" {
		t.Fatalf("secret round trip failed: plaintext=%q err=%v", plaintext, err)
	}
	wrongKey := append([]byte(nil), key...)
	wrongKey[0] ^= 0xff
	if _, err := openAdminSecret(wrongKey, ciphertext); err == nil {
		t.Fatal("secret decrypted with the wrong key")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer re_secret-never-return-this" || r.Header.Get("Idempotency-Key") != "export/1" {
			t.Errorf("unexpected Resend request method/auth: %s %q", r.Method, r.Header.Get("Authorization"))
		}
		var body struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			Text    string   `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode mail request: %v", err)
		}
		if body.From != "HeyGo <noreply@example.com>" || len(body.To) != 1 || body.To[0] != "driver@example.com" || body.Subject != "Archive ready" || body.Text == "" {
			t.Errorf("unexpected Resend email payload: %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := sendResendEmailTo(context.Background(), server.URL, "export/1", plaintext, "noreply@example.com", "HeyGo", "driver@example.com", "Archive ready", "Private link", "<p>Private link</p>", server.Client()); err != nil {
		t.Fatalf("Resend delivery failed: %v", err)
	}
}
