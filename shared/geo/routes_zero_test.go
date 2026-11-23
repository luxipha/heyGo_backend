package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOSRMAmendmentAllowsValidatedRoundedZeroTimeOnly(t *testing.T) {
	const actual = `{"code":"Ok","routes":[{"distance":0.1,"duration":0,"geometry":{"coordinates":[[3.357888,6.614659],[3.357888,6.61466]]}}],"waypoints":[{"location":[3.357888,6.614659]},{"location":[3.357888,6.61466]}]}`
	payload := actual
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) }))
	defer server.Close()
	router := OSRM{Endpoint: server.URL, Client: server.Client()}
	a, b := Coordinate{6.61466, 3.357888}, Coordinate{6.6143564, 3.3581327}
	if _, e := router.Route(context.Background(), a, b); e == nil {
		t.Fatal("ordinary routing accepted zero duration")
	}
	result, e := router.RouteAllowZero(context.Background(), a, b)
	if e != nil {
		t.Fatal(e)
	}
	if result.Provider != "osrm" || result.Routes[0].Distance != 0.1 || result.Routes[0].Duration != 0 {
		t.Fatal("actual provider measurements changed")
	}
	payload = strings.Replace(actual, `"distance":0.1`, `"distance":0`, 1)
	if _, e := router.RouteAllowZero(context.Background(), a, b); e != nil {
		t.Fatal("valid provider zero distance/time rejected", e)
	}
	bad := []string{
		strings.Replace(actual, `"distance":0.1,`, ``, 1),
		strings.Replace(actual, `"duration":0,`, ``, 1),
		`{"code":"NoRoute","routes":[]}`,
		`{"code":"Ok","routes":[{"distance":0.1,"duration":0,"geometry":{"coordinates":[[3.357888,6.614659],[3.357888,6.61466]]}}]}`,
		`{"code":"Ok","routes":[{"distance":1,"duration":0,"geometry":{"coordinates":[[3,6],[3,6]]}}],"waypoints":[{"location":[3,6]},{"location":[3,6]}]}`,
		`{"code":"Ok","routes":[{"distance":0.1,"duration":0,"geometry":{"coordinates":[[3,6],[4,7]]}}],"waypoints":[{"location":[3,6]},{"location":[4,7]}]}`,
		`{"code":"Ok","routes":[{"distance":0.1,"duration":0,"geometry":{"coordinates":[[3,6],[3,6]]}}],"waypoints":[{"location":[3,6]},{"location":[3.1,6]}]}`,
		`{"code":"Ok","routes":[{"distance":-0.1,"duration":0,"geometry":{"coordinates":[[3,6],[3,6]]}}],"waypoints":[{"location":[3,6]},{"location":[3,6]}]}`,
		`{"code":"Ok","routes":[{"distance":0.1,"duration":-1,"geometry":{"coordinates":[[3,6],[3,6]]}}],"waypoints":[{"location":[3,6]},{"location":[3,6]}]}`,
	}
	for _, v := range bad {
		payload = v
		if _, e := router.RouteAllowZero(context.Background(), a, b); e == nil {
			t.Fatalf("invalid zero-time response accepted: %s", v)
		}
	}
	payload = actual
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := router.RouteAllowZero(ctx, a, b); e == nil {
		t.Fatal("cancelled provider call accepted")
	}
}
