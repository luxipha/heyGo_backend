package geo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

const amapFixture = `{"status":"1","route":{"paths":[{"distance":"1400","duration":"420","steps":[{"polyline":"3.39,6.45;3.40,6.50"},{"polyline":"3.40,6.50;3.41,6.51"}]}]}}`

func TestAMapRouteContract(t *testing.T) {
	g := AMapRoutes{Key: "private", Client: &http.Client{Transport: googleTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.URL.Host != "sg-restapi.opnavi.com" || q.Get("origin") != "3.390000,6.450000" || q.Get("key") != "private" || q.Get("strategy") != "33" {
			t.Fatal("incorrect overseas request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(amapFixture))}, nil
	})}}
	result, err := g.Route(context.Background(), Coordinate{Latitude: 6.45, Longitude: 3.39}, Coordinate{Latitude: 6.51, Longitude: 3.41})
	if err != nil {
		t.Fatal(err)
	}
	r := result.Routes[0]
	if result.Provider != "amap" || r.Distance != 1400 || r.Duration != 420 || len(r.Geometry.Coordinates) != 3 || r.TrafficAware {
		t.Fatalf("invalid normalized route %+v", r)
	}
}
func TestAMapFailsClosed(t *testing.T) {
	for _, body := range []string{`{"status":"0","info":"private"}`, strings.Replace(amapFixture, "1400", "NaN", 1), strings.Replace(amapFixture, "3.39", "181", 1), `{"status":"1","route":{"paths":[]}}`} {
		g := AMapRoutes{Key: "private", Client: &http.Client{Transport: googleTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		if _, err := g.Route(context.Background(), Coordinate{}, Coordinate{Latitude: 1}); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe route failure", err)
		}
	}
}

func TestAMapSelectionAndMissingCredential(t *testing.T) {
	t.Setenv("GEO_ROUTE_PROVIDER", "amap")
	t.Setenv("AMAP_ROUTES_API_KEY", "")
	s := DefaultSettings()
	s.PlaceProvider = "google"
	if s.RouteProvider != "amap" || s.Validate() != nil {
		t.Fatal("AMap settings rejected")
	}
	if _, err := ConfiguredRoutes(); err == nil {
		t.Fatal("unconfigured AMap activated")
	}
	t.Setenv("AMAP_ROUTES_API_KEY", "private")
	router, err := ConfiguredRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := router.(*AMapRoutes); !ok {
		t.Fatal("wrong routing provider")
	}
}
