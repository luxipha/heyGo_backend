package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredRoutesFailsClosed(t *testing.T) {
	t.Setenv("OSRM_ALLOW_DEMO", "false")
	t.Setenv("GEO_ROUTE_PROVIDER", "osrm")
	t.Setenv("ENV", "production")
	t.Setenv("ENVIRONMENT", "")
	for _, endpoint := range []string{"", "http://router.project-osrm.org/route/v1/driving", "https://router.project-osrm.org/route/v1/driving", "ftp://private/route/v1/driving"} {
		t.Setenv("OSRM_API", endpoint)
		if _, err := ConfiguredRoutes(); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
	t.Setenv("OSRM_API", "https://routing.example.com/route/v1/driving")
	if _, err := ConfiguredRoutes(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEO_ROUTE_PROVIDER", "disabled")
	if _, err := ConfiguredRoutes(); err == nil {
		t.Fatal("disabled provider enabled")
	}
}
func TestOSRMContract(t *testing.T) {
	payload := `{"code":"Ok","routes":[{"distance":1000,"duration":300,"geometry":{"coordinates":[[3.39,6.45],[3.4,6.5]]},"legs":[{"steps":[{"distance":200,"duration":60,"name":"Gate Road","maneuver":{"type":"turn","modifier":"left","location":[3.39,6.45]}}]}]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "3.390000,6.450000;3.400000,6.500000") || r.URL.Query().Get("geometries") != "geojson" || r.URL.Query().Get("steps") != "true" {
			t.Error("incorrect provider coordinate/geometry mapping")
		}
		w.Write([]byte(payload))
	}))
	defer server.Close()
	adapter := OSRM{Endpoint: server.URL, Client: server.Client()}
	a, b := Coordinate{6.45, 3.39}, Coordinate{6.5, 3.4}
	result, err := adapter.Route(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "osrm" || result.Routes[0].Distance != 1000 || len(result.Routes[0].Maneuvers) != 1 {
		t.Fatalf("invalid normalized result: %+v", result)
	}
	for _, bad := range []string{`{"code":"NoRoute","routes":[]}`, `{"code":"Ok","routes":[{"distance":-1,"duration":2,"geometry":{"coordinates":[[3,6],[3,6]]}}]}`, `{"code":"Ok","routes":[{"distance":1,"duration":2,"geometry":{"coordinates":[[181,6],[3,6]]}}]}`} {
		payload = bad
		if _, err := adapter.Route(context.Background(), a, b); err == nil {
			t.Error("invalid provider route accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Route(ctx, a, b); err == nil {
		t.Error("cancelled request succeeded")
	}
}
func TestPolyline(t *testing.T) {
	if got := Polyline([][]float64{{-120.2, 38.5}, {-120.95, 40.7}, {-126.453, 43.252}}); got != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Fatal(got)
	}
}
