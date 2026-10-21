package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Settings struct {
	RouteProvider      string `json:"routeProvider"`
	OSRMEndpoint       string `json:"osrmEndpoint"`
	AllowDemo          bool   `json:"allowDemo"`
	PlaceProvider      string `json:"placeProvider"`
	MapProvider        string `json:"mapProvider"`
	NavigationProvider string `json:"navigationProvider"`
}
type SettingsStatus struct {
	Settings Settings `json:"settings"`
	Version  int64    `json:"version"`
	Source   string   `json:"source"`
}

func DefaultSettings() Settings {
	endpoint := strings.TrimSpace(os.Getenv("OSRM_API"))
	// Keep the previously deployed demo available as an explicit Admin testing choice.
	if endpoint == "" {
		endpoint = "https://router.project-osrm.org/route/v1/driving"
	}
	return Settings{RouteProvider: "osrm", OSRMEndpoint: endpoint, AllowDemo: os.Getenv("OSRM_ALLOW_DEMO") == "true", PlaceProvider: "google", MapProvider: "google", NavigationProvider: "google"}
}
func (s Settings) Validate() error {
	if s.RouteProvider != "osrm" && s.RouteProvider != "disabled" {
		return errors.New("unsupported route provider")
	}
	for _, provider := range []string{s.PlaceProvider, s.MapProvider, s.NavigationProvider} {
		if provider != "google" && provider != "disabled" {
			return errors.New("unsupported Google capability provider")
		}
	}
	if s.RouteProvider == "disabled" && s.OSRMEndpoint == "" {
		return nil
	}
	u, err := url.Parse(s.OSRMEndpoint)
	if err != nil || len(s.OSRMEndpoint) > 512 || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s.OSRMEndpoint, "\\\r\n") {
		return errors.New("valid OSRM route endpoint required")
	}
	demo := strings.EqualFold(u.Hostname(), "router.project-osrm.org")
	if demo {
		if !s.AllowDemo || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
			return errors.New("enable temporary demo mode to use the public OSRM demo")
		}
	} else {
		if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") || !strings.Contains(u.Hostname(), ".") || strings.HasSuffix(u.Hostname(), ".internal") || strings.HasSuffix(u.Hostname(), ".local") || strings.EqualFold(u.Hostname(), "metadata.google.internal") {
			return errors.New("custom OSRM endpoint must be a public HTTPS hostname")
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			return errors.New("use a public DNS hostname for OSRM")
		}
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/route/v1/driving") {
		return errors.New("OSRM endpoint must end with /route/v1/driving")
	}
	return nil
}
func ReadSettings(ctx context.Context, pool *pgxpool.Pool) (SettingsStatus, error) {
	if pool == nil {
		return SettingsStatus{}, errors.New("geo settings database unavailable")
	}
	var raw []byte
	var version int64
	err := pool.QueryRow(ctx, `SELECT configuration,version FROM admin_geo_settings WHERE id=1`).Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return SettingsStatus{Settings: DefaultSettings(), Source: "environment", Version: 0}, nil
	}
	if err != nil {
		return SettingsStatus{}, err
	}
	var s Settings
	if json.Unmarshal(raw, &s) != nil {
		return SettingsStatus{}, errors.New("invalid saved geo settings")
	}
	return SettingsStatus{Settings: s, Version: version, Source: "admin"}, nil
}

// DatabaseRoutes reads current Admin configuration for each new quote/route.
// Existing quotes retain their original distance, duration and price.
type DatabaseRoutes struct{ Pool *pgxpool.Pool }

func (d DatabaseRoutes) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	status, err := ReadSettings(ctx, d.Pool)
	if err != nil {
		return nil, err
	}
	s := status.Settings
	if err = s.Validate(); err != nil {
		return nil, err
	}
	if s.RouteProvider != "osrm" {
		return nil, errors.New("routing disabled by administrator")
	}
	return (&OSRM{Endpoint: s.OSRMEndpoint, Client: routingHTTPClient}).Route(ctx, a, b)
}

// Resolve at dial time and disallow private/metadata addresses, including DNS rebinding.
var routingHTTPClient = publicRoutingClient()

func publicRoutingClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("routing DNS unavailable")
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				return nil, errors.New("private routing address rejected")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, fmt.Errorf("routing connection failed: %w", last)
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
