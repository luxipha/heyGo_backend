package ideas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestIdeasClientConfigAndRedirectSafety(t *testing.T) {
	t.Setenv("HEYGO_IDEAS_ENABLED", "false")
	if _, e := Configured(); e == nil {
		t.Fatal("disabled bridge accepted")
	}
	t.Setenv("HEYGO_IDEAS_ENABLED", "true")
	t.Setenv("HEYGO_IDEAS_TOKEN", "secret-test-token")
	for _, u := range []string{"http://ideas.example", "https://user:pass@ideas.example", "https://ideas.example?token=x"} {
		t.Setenv("HEYGO_IDEAS_URL", u)
		if _, e := Configured(); e == nil {
			t.Fatal("unsafe URL", u)
		}
	}
	var redirected atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-test-token" {
			t.Error("missing server auth")
		}
		if r.URL.Query().Get("board") != "riders" {
			t.Error("missing board scope")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, 307)
			return
		}
		w.Write([]byte(`{"voted":true}`))
	}))
	defer server.Close()
	t.Setenv("HEYGO_IDEAS_URL", server.URL)
	t.Setenv("HEYGO_IDEAS_BOARD", "riders")
	c, e := Configured()
	if e != nil {
		t.Fatal(e)
	}
	c.HTTP.Transport = server.Client().Transport
	var result struct {
		Voted bool `json:"voted"`
	}
	if e = c.Post(context.Background(), "/vote", map[string]string{"riderId": "owned"}, &result); e != nil || !result.Voted {
		t.Fatal(result, e)
	}
	if e = c.Post(context.Background(), "/redirect", nil, &result); e == nil || redirected.Load() != 0 {
		t.Fatal("credential redirect followed", e)
	}
}
