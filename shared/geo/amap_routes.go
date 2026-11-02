package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const amapRoutesURL = "https://sg-restapi.opnavi.com/v3/direction/driving"

// AMapRoutes uses the documented overseas endpoint. Nigeria coordinates stay
// longitude/latitude; no mainland-China coordinate conversion is applied.
type AMapRoutes struct {
	Key    string
	Client *http.Client
}

func configuredAMapRoutes() (RouteService, error) {
	key := strings.TrimSpace(os.Getenv("AMAP_ROUTES_API_KEY"))
	if key == "" {
		return nil, errors.New("AMap Routes backend key is not configured")
	}
	return &AMapRoutes{Key: key}, nil
}
func (g *AMapRoutes) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	if !a.Valid() || !b.Valid() {
		return nil, errors.New("invalid route coordinates")
	}
	if strings.TrimSpace(g.Key) == "" {
		return nil, errors.New("AMap Routes backend key is not configured")
	}
	q := url.Values{"key": {g.Key}, "origin": {fmt.Sprintf("%.6f,%.6f", a.Longitude, a.Latitude)}, "destination": {fmt.Sprintf("%.6f,%.6f", b.Longitude, b.Latitude)}, "strategy": {"33"}, "extensions": {"all"}, "langCode": {"en"}, "output": {"JSON"}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, amapRoutesURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("invalid AMap route request")
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	// Provider-required key is in the URL; never expose transport errors or bodies.
	if err != nil {
		return nil, errors.New("AMap Routes request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("AMap Routes returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, errors.New("route response too large or unreadable")
	}
	var wire struct {
		Status string `json:"status"`
		Route  struct {
			Paths []struct {
				Distance string `json:"distance"`
				Duration string `json:"duration"`
				Steps    []struct {
					Polyline string `json:"polyline"`
				} `json:"steps"`
			} `json:"paths"`
		} `json:"route"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Status != "1" || len(wire.Route.Paths) == 0 {
		return nil, errors.New("no usable AMap route")
	}
	w := wire.Route.Paths[0]
	distance, e1 := strconv.ParseFloat(w.Distance, 64)
	duration, e2 := strconv.ParseFloat(w.Duration, 64)
	if e1 != nil || e2 != nil || !positive(distance) || !positive(duration) {
		return nil, errors.New("invalid AMap route measurements")
	}
	var points [][]float64
	for _, step := range w.Steps {
		for _, pair := range strings.Split(step.Polyline, ";") {
			values := strings.Split(pair, ",")
			if len(values) != 2 {
				return nil, errors.New("invalid AMap route geometry")
			}
			lng, e1 := strconv.ParseFloat(values[0], 64)
			lat, e2 := strconv.ParseFloat(values[1], 64)
			if e1 != nil || e2 != nil || !(Coordinate{Latitude: lat, Longitude: lng}).Valid() {
				return nil, errors.New("invalid AMap route geometry")
			}
			if len(points) > 0 && points[len(points)-1][0] == lng && points[len(points)-1][1] == lat {
				continue
			}
			points = append(points, []float64{lng, lat})
		}
	}
	if len(points) < 2 {
		return nil, errors.New("invalid AMap route geometry")
	}
	// A congestion-avoidance request is not proof of live traffic coverage.
	// No static duration/traffic-aware claim is inferred without provider evidence.
	return &RouteResponse{Provider: "amap", Routes: []Route{{Distance: distance, Duration: duration, Geometry: Geometry{Coordinates: points}}}}, nil
}
