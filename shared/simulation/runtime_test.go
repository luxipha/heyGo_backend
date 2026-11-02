package simulation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeClientBoundaries(t *testing.T) {
	for _, status := range []int{200, 403, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing actor authentication")
				}
				if status == 302 {
					w.Header().Set("Location", "http://untrusted.example")
				}
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "offline"}, "error": map[string]any{"code": "driver_not_eligible"}})
			}))
			defer server.Close()
			r := Runtime{BaseURL: server.URL}
			a := &liveActor{Actor: Actor{Token: "test-token"}}
			value, e := r.request(context.Background(), a, "POST", "/driver/availability", map[string]bool{"available": true})
			if status == 200 {
				if e != nil || value["status"] != "offline" {
					t.Fatal("bad API result")
				}
			} else if e == nil {
				t.Fatal("denial/redirect accepted")
			}
		})
	}
}
func TestRunIdentityScope(t *testing.T) {
	run := "a9bab02e-1c4a-4a84-9202-cf68b9e74f95"
	participant := "0d0b79ec-7cef-45bd-b2e6-c7786e05f806"
	if identity(run, participant) != identity(run, participant) {
		t.Fatal("identity unstable")
	}
	if identity(run, participant) == identity("other-run", participant) {
		t.Fatal("identity crosses runs")
	}
	if identity(run, participant) == identity(run, "other-participant") {
		t.Fatal("identity crosses participants")
	}
}

func TestMovementFollowsRoadSegmentsAndStopsAtEndpoint(t *testing.T) {
	m := movement{points: []point{{6.428, 3.421}, {6.429, 3.421}, {6.429, 3.422}}, position: point{6.428, 3.421}, index: 1}
	p := m.advance(50)
	if p.Lng != 3.421 || p.Lat <= 6.428 || p.Lat >= 6.429 || m.finished {
		t.Fatal("cut corner or advanced incorrectly")
	}
	p = m.advance(500)
	if p != m.points[2] || !m.finished {
		t.Fatal("failed to stop at final road point")
	}
}
