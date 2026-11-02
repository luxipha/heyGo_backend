package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"net/url"
	"os"
	"strings"
	"time"
)

// PlaceService is the stable search/resolve boundary for all applications.
type PlaceService = Provider

func ConfiguredPlaces() (PlaceService, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("GEO_PLACE_PROVIDER")))
	if provider == "" {
		provider = "google"
	}
	return placesFor(provider, nil)
}

func placesFor(provider string, pool *pgxpool.Pool) (PlaceService, error) {
	if provider == "disabled" {
		return nil, nil
	}
	if provider == "geoapify" {
		key := strings.TrimSpace(os.Getenv("GEOAPIFY_API_KEY"))
		if key == "" {
			return nil, fmt.Errorf("Geoapify key is required")
		}
		var store GeoapifyPlaceStore
		if pool != nil {
			store = PostgresNominatimStore{Pool: pool}
		}
		return GeoapifyPlaces{Key: key, Store: store}, nil
	}
	if provider == "nominatim" {
		endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("NOMINATIM_BASE_URL")), "/")
		if endpoint == "" {
			endpoint = "https://nominatim.openstreetmap.org"
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(endpoint, "\\\r\n") {
			return nil, fmt.Errorf("Nominatim requires an HTTPS base URL without credentials or query parameters")
		}
		if strings.EqualFold(u.Hostname(), "nominatim.openstreetmap.org") && (u.Port() != "" || u.Path != "" || u.Hostname() != "nominatim.openstreetmap.org") {
			return nil, fmt.Errorf("use the canonical public Nominatim URL")
		}
		userAgent := strings.TrimSpace(os.Getenv("NOMINATIM_USER_AGENT"))
		if userAgent == "" {
			userAgent = "HeyGo/1.0 (+https://heygo.ng)"
		}
		if len(userAgent) > 256 || strings.ContainsAny(userAgent, "\r\n") {
			return nil, fmt.Errorf("invalid Nominatim application identity")
		}
		return NominatimPlaces{BaseURL: endpoint, UserAgent: userAgent, Store: PostgresNominatimStore{Pool: pool}}, nil
	}
	if provider != "google" {
		return nil, fmt.Errorf("unsupported places provider")
	}
	key := strings.TrimSpace(os.Getenv("RIDE_AGENT_PLACES_KEY"))
	if key == "" {
		return nil, fmt.Errorf("Google Places key is required")
	}
	return GooglePlaces{Key: key}, nil
}

// Admin selects the provider per request. Provider references retain their origin
// across configuration changes, so a saved OSM place never goes to Google.
type DatabasePlaces struct{ Pool *pgxpool.Pool }

// Selection resolves through its original provider even after the search order changes.
// Global disable blocks resolution too; individual search switches do not orphan saved places.
func (p DatabasePlaces) provider(ctx context.Context, id string) (PlaceService, error) {
	status, err := geo.ReadSettings(ctx, p.Pool)
	if err != nil {
		return nil, err
	}
	if !status.Settings.PlacesEnabled() {
		return nil, errors.New("place search disabled")
	}
	name := status.Settings.PlaceProvider
	if id != "" {
		name = "google"
		if strings.HasPrefix(id, "osm_") {
			name = "nominatim"
		} else if strings.HasPrefix(id, "geoapify_") {
			name = "geoapify"
		}
	}
	return placesFor(name, p.Pool)
}
func (p DatabasePlaces) Resolve(ctx context.Context, id string) (Place, error) {
	return p.ResolveSession(ctx, id, "")
}
func (p DatabasePlaces) ResolveSession(ctx context.Context, id, token string) (Place, error) {
	provider, err := p.provider(ctx, id)
	if err != nil {
		return Place{}, err
	}
	if resolver, ok := provider.(SessionResolver); ok {
		return resolver.ResolveSession(ctx, id, token)
	}
	return provider.Resolve(ctx, id)
}
func (p DatabasePlaces) chain(ctx context.Context) (PlaceChain, error) {
	status, err := geo.ReadSettings(ctx, p.Pool)
	if err != nil {
		return PlaceChain{}, err
	}
	enabled := status.Settings.EnabledPlaces()
	flags := map[string]bool{"google": enabled.Google, "geoapify": enabled.Geoapify, "nominatim": enabled.Nominatim}
	chain := PlaceChain{}
	seen := map[string]bool{}
	for _, name := range []string{status.Settings.PlaceProvider, "google", "geoapify", "nominatim"} {
		if seen[name] || !flags[name] {
			continue
		}
		seen[name] = true
		provider, e := placesFor(name, p.Pool)
		if e == nil && provider != nil {
			chain.Providers = append(chain.Providers, NamedPlaces{name, provider})
		}
	}
	return chain, nil
}
func (p DatabasePlaces) Search(ctx context.Context, q string, bias *Coordinate) ([]Place, error) {
	chain, err := p.chain(ctx)
	if err != nil {
		return nil, err
	}
	return chain.Search(ctx, q, bias)
}
func (p DatabasePlaces) Suggest(ctx context.Context, q string, bias *Coordinate, token string) ([]Suggestion, error) {
	chain, err := p.chain(ctx)
	if err != nil {
		return nil, err
	}
	return chain.Suggest(ctx, q, bias, token)
}

type Suggestion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
}
type AutocompleteProvider interface {
	Suggest(context.Context, string, *Coordinate, string) ([]Suggestion, error)
}
type SessionResolver interface {
	ResolveSession(context.Context, string, string) (Place, error)
}
type NamedPlaces struct {
	Name     string
	Provider PlaceService
}
type PlaceChain struct{ Providers []NamedPlaces }

func validSearch(q string, bias *Coordinate) bool {
	return len(strings.TrimSpace(q)) >= 3 && len(q) <= 160 && !sensitive.MatchString(q) && (bias == nil || ValidLocation(*bias))
}
func (p PlaceChain) Search(ctx context.Context, q string, bias *Coordinate) ([]Place, error) {
	if !validSearch(q, bias) {
		return nil, errors.New("invalid place query")
	}
	var empty bool
	for _, provider := range p.Providers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if provider.Name == "nominatim" && ctx.Value(noPublicPlacesKey{}) == true {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		places, err := provider.Provider.Search(attempt, q, bias)
		cancel()
		if err == nil {
			valid := make([]Place, 0, len(places))
			for _, place := range places {
				if placeID.MatchString(place.ID) && place.Name != "" && len(place.Name) <= 256 && len(place.Address) <= 1024 && ValidLocation(place.Location) {
					valid = append(valid, place)
				}
			}
			places = valid
			empty = true
			if len(places) > 0 {
				return places, nil
			}
		}
	}
	if empty {
		return []Place{}, nil
	}
	return nil, errors.New("place search unavailable")
}
func (p PlaceChain) Suggest(ctx context.Context, q string, bias *Coordinate, token string) ([]Suggestion, error) {
	if !validSearch(q, bias) {
		return nil, errors.New("invalid place query")
	}
	var empty bool
	for _, provider := range p.Providers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Public Nominatim must never receive typeahead, regardless of Admin switches.
		if provider.Name == "nominatim" {
			continue
		}
		autocomplete, ok := provider.Provider.(AutocompleteProvider)
		if !ok {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		results, err := autocomplete.Suggest(attempt, q, bias, token)
		cancel()
		if err == nil {
			valid := make([]Suggestion, 0, len(results))
			for _, result := range results {
				if placeID.MatchString(result.ID) && result.Name != "" && len(result.Name) <= 256 && len(result.Address) <= 1024 {
					valid = append(valid, result)
				}
			}
			results = valid
			empty = true
			if len(results) > 0 {
				return results, nil
			}
		}
	}
	if empty {
		return []Suggestion{}, nil
	}
	return nil, errors.New("autocomplete unavailable; use explicit search")
}

type noPublicPlacesKey struct{}
