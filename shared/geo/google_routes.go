package geo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const googleRoutesURL = "https://routes.googleapis.com/directions/v2:computeRoutes"

// GoogleRoutes adapts traffic-aware Google routes to the existing route contract.
// Keys stay in server configuration. There is no implicit paid fallback.
type GoogleRoutes struct {
	Key    string
	Client *http.Client
}

func configuredGoogleRoutes() (RouteService, error) {
	key := strings.TrimSpace(os.Getenv("GOOGLE_ROUTES_API_KEY"))
	if key == "" {
		return nil, errors.New("Google Routes backend key is not configured")
	}
	return &GoogleRoutes{Key: key}, nil
}

func (g *GoogleRoutes) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	if !a.Valid() || !b.Valid() {
		return nil, errors.New("invalid route coordinates")
	}
	if strings.TrimSpace(g.Key) == "" {
		return nil, errors.New("Google Routes backend key is not configured")
	}
	waypoint := func(c Coordinate) any { return map[string]any{"location": map[string]any{"latLng": c}} }
	body, err := json.Marshal(map[string]any{"origin": waypoint(a), "destination": waypoint(b), "travelMode": "DRIVE", "routingPreference": "TRAFFIC_AWARE", "polylineEncoding": "GEO_JSON_LINESTRING", "units": "METRIC"})
	if err != nil {
		return nil, errors.New("invalid route request")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleRoutesURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid route request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", g.Key)
	req.Header.Set("X-Goog-FieldMask", "routes.distanceMeters,routes.duration,routes.staticDuration,routes.polyline.geoJsonLinestring")
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Google Routes request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google Routes returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, errors.New("route response too large or unreadable")
	}
	var wire struct {
		Routes []struct {
			Distance       float64 `json:"distanceMeters"`
			Duration       string  `json:"duration"`
			StaticDuration string  `json:"staticDuration"`
			Polyline       struct {
				Geometry Geometry `json:"geoJsonLinestring"`
			} `json:"polyline"`
		} `json:"routes"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire.Routes) == 0 {
		return nil, errors.New("no usable Google route")
	}
	w := wire.Routes[0]
	duration, err := time.ParseDuration(w.Duration)
	if err != nil || !positive(duration.Seconds()) || !positive(w.Distance) || len(w.Polyline.Geometry.Coordinates) < 2 {
		return nil, errors.New("invalid Google route measurements")
	}
	base, err := time.ParseDuration(w.StaticDuration)
	if err != nil || !positive(base.Seconds()) {
		return nil, errors.New("invalid Google static duration")
	}
	for _, p := range w.Polyline.Geometry.Coordinates {
		if len(p) != 2 || !(Coordinate{Latitude: p[1], Longitude: p[0]}).Valid() {
			return nil, errors.New("invalid Google route geometry")
		}
	}
	return &RouteResponse{Provider: "google", Routes: []Route{{Distance: w.Distance, Duration: duration.Seconds(), StaticDuration: base.Seconds(), TrafficAware: true, Geometry: w.Polyline.Geometry}}}, nil
}
