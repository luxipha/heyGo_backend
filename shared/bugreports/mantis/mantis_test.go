package mantis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxipha/heyGo_backend/shared/bugreports"
)

func TestAdapterContractAndPrivacy(t *testing.T) {
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "HeyGo-BugReports/1.0" {
			t.Error("missing integration user-agent")
		}
		if r.Header.Get("Authorization") != "test-token" {
			t.Error("missing server credential")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"issues":[{"id":12,"project":{"id":2},"summary":"Cannot book","description":"App closes on booking","status":{"id":50},"reporter":{"email":"private@example.com"},"notes":[{"text":"internal secret","view_state":{"id":50}},{"text":"Please update the app","created_at":"2026-10-18T00:00:00Z","view_state":{"id":10}},{"text":"unknown visibility"}]}]}`))
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request JSON")
		}
		if r.Method == "POST" && r.URL.Path == "/api/rest/issues" {
			if body["view_state"].(map[string]any)["id"] != float64(50) || body["project"].(map[string]any)["id"] != float64(2) || body["category"].(map[string]any)["id"] != float64(1) {
				t.Error("wrong project/category/privacy")
			}
			if body["additional_information"] != "HeyGo reference: ref" {
				t.Error("missing recovery reference")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"issue":{"id":12}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/notes") && body["view_state"].(map[string]any)["id"] != float64(10) {
			t.Error("customer update is not public")
		}
		if r.Method == "PATCH" && body["status"].(map[string]any)["id"] != float64(80) {
			t.Error("status was not translated")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	a, err := New(Config{URL: server.URL, Token: "test-token", ProjectID: 2, CategoryID: 1})
	if err != nil {
		t.Fatal(err)
	}
	a.client = server.Client()
	id, err := a.Create(context.Background(), bugreports.Input{Title: "Cannot book", Description: "App closes on booking", Reference: "ref"})
	if err != nil || id != "12" {
		t.Fatalf("create=%s %v", id, err)
	}
	report, err := a.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "email") || strings.Contains(string(raw), "unknown visibility") {
		t.Fatalf("private provider data escaped: %s", raw)
	}
	if report.Status != "in_progress" || len(report.Comments) != 1 {
		t.Fatalf("incorrect normalized report: %+v", report)
	}
	if err = a.Comment(context.Background(), id, "Please update the app"); err != nil {
		t.Fatal(err)
	}
	if err = a.SetStatus(context.Background(), id, "resolved"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("unexpected provider calls: %v", calls)
	}
}
func TestAdapterRejectsForeignProjectAndErrors(t *testing.T) {
	for _, response := range []struct {
		code int
		body string
	}{{200, `{"issues":[{"id":12,"project":{"id":1}}]}`}, {200, `{"issues":[]}`}, {200, `not json`}, {403, `{"message":"credential-sensitive provider detail"}`}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(response.code)
			w.Write([]byte(response.body))
		}))
		a, _ := New(Config{URL: server.URL, Token: "test-token", ProjectID: 2, CategoryID: 1})
		a.client = server.Client()
		if _, err := a.Get(context.Background(), "12"); err == nil || strings.Contains(err.Error(), "credential-sensitive") {
			t.Errorf("unsafe error: %v", err)
		}
		server.Close()
	}
}
func TestConfigurationAndRedirects(t *testing.T) {
	for _, u := range []string{"http://tracker.test", "https://user:password@tracker.test", "https://tracker.test?token=x"} {
		if _, err := New(Config{URL: u, Token: "token", ProjectID: 2, CategoryID: 1}); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
	a, err := New(Config{URL: "https://tracker.test", Token: "token", ProjectID: 2, CategoryID: 1})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := New(Config{URL: "https://another-tracker.test", Token: "token", ProjectID: 2, CategoryID: 1})
	if a.Name() == other.Name() {
		t.Fatal("different tracker instances share ownership references")
	}
	if a.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("credentials may follow redirects")
	}
	if err := a.SetStatus(context.Background(), "12", "unknown"); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestCustomFieldsAreValidatedCopiedAndSent(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			CustomFields []struct {
				Field struct {
					ID int `json:"id"`
				} `json:"field"`
				Value string `json:"value"`
			} `json:"custom_fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if len(b.CustomFields) != 3 || b.CustomFields[0].Field.ID != 1 || b.CustomFields[0].Value != "Rider" || b.CustomFields[1].Value != "Rider App" || b.CustomFields[2].Value != "Unknown" {
			t.Errorf("wrong provider field mapping: %+v", b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.Write([]byte(`{"issue":{"id":15}}`))
	}))
	defer server.Close()
	fields := map[int]string{1: "Rider", 2: "Rider App", 4: "Unknown"}
	a, err := New(Config{URL: server.URL, Token: "test", ProjectID: 2, CategoryID: 2, CustomFields: fields})
	if err != nil {
		t.Fatal(err)
	}
	a.client = server.Client()
	fields[1] = "Driver"
	if _, err = a.Create(context.Background(), bugreports.Input{Title: "Test", Description: "Controlled test", Reference: "ref"}); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[int]string{{0: "Rider"}, {-1: "Rider"}, {1: strings.Repeat("x", 256)}} {
		if _, err = New(Config{URL: server.URL, Token: "test", ProjectID: 2, CategoryID: 2, CustomFields: fields}); err == nil {
			t.Fatal("accepted invalid mapping")
		}
	}
}
