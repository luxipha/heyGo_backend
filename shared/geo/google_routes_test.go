package geo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type googleTransport func(*http.Request) (*http.Response, error)

func (f googleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const googleRouteFixture = `{"routes":[{"distanceMeters":1400,"duration":"420s","staticDuration":"300s","polyline":{"geoJsonLinestring":{"type":"LineString","coordinates":[[3.39,6.45],[3.4,6.5]]}}}]}`

func TestGoogleRoutesContract(t *testing.T) {
	client := &http.Client{Transport: googleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != googleRoutesURL || r.Method != "POST" || r.Header.Get("X-Goog-Api-Key") != "test-secret" || r.Header.Get("X-Goog-FieldMask") == "" {
			t.Fatal("invalid request")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["routingPreference"] != "TRAFFIC_AWARE" || body["polylineEncoding"] != "GEO_JSON_LINESTRING" {
			t.Fatal("invalid traffic request")
		}
		if r.URL.RawQuery != "" {
			t.Fatal("key in URL")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(googleRouteFixture))}, nil
	})}
	result, err := (&GoogleRoutes{Key: "test-secret", Client: client}).Route(context.Background(), Coordinate{Latitude: 6.45, Longitude: 3.39}, Coordinate{Latitude: 6.5, Longitude: 3.4})
	if err != nil {
		t.Fatal(err)
	}
	r := result.Routes[0]
	if result.Provider != "google" || r.Duration != 420 || r.StaticDuration != 300 || !r.TrafficAware || r.Distance != 1400 || len(r.Geometry.Coordinates) != 2 {
		t.Fatalf("invalid normalized route: %+v", r)
	}
}
func TestGoogleRoutesFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"empty", `{"routes":[]}`, 200},
		{"malformed", `{`, 200},
		{"bad duration", strings.Replace(googleRouteFixture, "420s", "invalid", 1), 200},
		{"negative", strings.Replace(googleRouteFixture, "1400", "-1", 1), 200},
		{"missing static", strings.Replace(googleRouteFixture, "300s", "", 1), 200},
		{"bad coordinates", strings.Replace(googleRouteFixture, "3.39", "181", 1), 200},
		{"quota", `{"error":"test-secret"}`, 429},
		{"denied", `{"error":"test-secret"}`, 403},
		{"oversize", strings.Repeat(" ", (2<<20)+1), 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := GoogleRoutes{Key: "test-secret", Client: &http.Client{Transport: googleTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}}
			_, err := g.Route(context.Background(), Coordinate{}, Coordinate{Latitude: 1})
			if err == nil || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}
func TestGoogleRoutesMissingKey(t *testing.T) {
	t.Setenv("GOOGLE_ROUTES_API_KEY", "")
	if _, err := configuredGoogleRoutes(); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestGoogleRoutesSelection(t *testing.T) {
	s := Settings{RouteProvider: "google", PlaceProvider: "google", MapProvider: "google", NavigationProvider: "google"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEO_ROUTE_PROVIDER", "google")
	t.Setenv("GOOGLE_ROUTES_API_KEY", "test-secret")
	if DefaultSettings().RouteProvider != "google" {
		t.Fatal("environment provider ignored")
	}
	router, err := ConfiguredRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := router.(*GoogleRoutes); !ok {
		t.Fatal("wrong provider")
	}
	t.Setenv("GOOGLE_ROUTES_API_KEY", "")
	if _, err := ConfiguredRoutes(); err == nil {
		t.Fatal("silently fell back without key")
	}
}
