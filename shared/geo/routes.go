// Package geo owns provider-neutral geographic contracts. No paid fallback is implicit.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Coordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (p Coordinate) Valid() bool {
	return !math.IsNaN(p.Latitude) && !math.IsNaN(p.Longitude) && !math.IsInf(p.Latitude, 0) && !math.IsInf(p.Longitude, 0) && math.Abs(p.Latitude) <= 90 && math.Abs(p.Longitude) <= 180
}

type Geometry struct {
	Coordinates [][]float64 `json:"coordinates"`
}
type Maneuver struct {
	Type     string    `json:"type"`
	Modifier string    `json:"modifier"`
	RoadName string    `json:"roadName"`
	Location []float64 `json:"location"`
	Distance float64   `json:"distanceMeters"`
	Duration float64   `json:"durationSeconds"`
}
type Route struct {
	Distance  float64    `json:"distance"`
	Duration  float64    `json:"duration"`
	Geometry  Geometry   `json:"geometry"`
	Maneuvers []Maneuver `json:"maneuvers,omitempty"`
}
type RouteResponse struct {
	Routes   []Route `json:"routes"`
	Provider string  `json:"provider,omitempty"`
}
type RouteService interface {
	Route(context.Context, Coordinate, Coordinate) (*RouteResponse, error)
}
type OSRM struct {
	Endpoint string
	Client   *http.Client
}

// ConfiguredRoutes requires an explicit endpoint. Temporary demo use in production
// must be deliberately enabled for testing; no paid fallback is implicit.
func ConfiguredRoutes() (RouteService, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("GEO_ROUTE_PROVIDER")))
	if provider == "" {
		provider = "osrm"
	}
	if provider != "osrm" {
		return nil, errors.New("route provider disabled or unsupported")
	}
	endpoint := strings.TrimSpace(os.Getenv("OSRM_API"))
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OSRM_API must be an explicit route endpoint")
	}
	production := os.Getenv("ENV") == "production" || os.Getenv("ENVIRONMENT") == "production"
	demo := strings.EqualFold(u.Hostname(), "router.project-osrm.org")
	allowDemo := os.Getenv("OSRM_ALLOW_DEMO") == "true"
	if production && ((demo && !allowDemo) || (!demo && u.Scheme != "https")) {
		return nil, errors.New("production requires a supported HTTPS OSRM endpoint")
	}
	return &OSRM{Endpoint: strings.TrimRight(endpoint, "/"), Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (o *OSRM) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	if !a.Valid() || !b.Valid() {
		return nil, errors.New("invalid route coordinates")
	}
	u := fmt.Sprintf("%s/%.6f,%.6f;%.6f,%.6f?overview=full&geometries=geojson&steps=true", strings.TrimRight(o.Endpoint, "/"), a.Longitude, a.Latitude, b.Longitude, b.Latitude)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("routing returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, errors.New("route response too large or unreadable")
	}
	var wire struct {
		Code   string
		Routes []struct {
			Distance, Duration float64
			Geometry           Geometry
			Legs               []struct {
				Steps []struct {
					Distance, Duration float64
					Name               string
					Maneuver           struct {
						Type, Modifier string
						Location       []float64
					}
				}
			}
		}
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Code != "Ok" || len(wire.Routes) == 0 {
		return nil, errors.New("no usable route")
	}
	w := wire.Routes[0]
	if !positive(w.Distance) || !positive(w.Duration) || len(w.Geometry.Coordinates) < 2 {
		return nil, errors.New("invalid route measurements")
	}
	r := Route{Distance: w.Distance, Duration: w.Duration, Geometry: w.Geometry}
	for _, p := range r.Geometry.Coordinates {
		if len(p) != 2 || !(Coordinate{Latitude: p[1], Longitude: p[0]}).Valid() {
			return nil, errors.New("invalid route geometry")
		}
	}
	for _, l := range w.Legs {
		for _, s := range l.Steps {
			if len(s.Maneuver.Location) != 2 || !(Coordinate{Latitude: s.Maneuver.Location[1], Longitude: s.Maneuver.Location[0]}).Valid() || s.Distance < 0 || s.Duration < 0 {
				return nil, errors.New("invalid route maneuver")
			}
			r.Maneuvers = append(r.Maneuvers, Maneuver{Type: s.Maneuver.Type, Modifier: s.Maneuver.Modifier, RoadName: s.Name, Location: s.Maneuver.Location, Distance: s.Distance, Duration: s.Duration})
		}
	}
	return &RouteResponse{Routes: []Route{r}, Provider: "osrm"}, nil
}
func positive(n float64) bool { return n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }

// Polyline encodes the normalized longitude/latitude geometry for existing mobile clients.
func Polyline(points [][]float64) string {
	var out strings.Builder
	lat, lng := 0, 0
	appendValue := func(v int) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			out.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
			v >>= 5
		}
		out.WriteByte(byte(v + 63))
	}
	for _, p := range points {
		if len(p) != 2 {
			continue
		}
		a, b := int(math.Round(p[1]*1e5)), int(math.Round(p[0]*1e5))
		appendValue(a - lat)
		appendValue(b - lng)
		lat, lng = a, b
	}
	return out.String()
}
