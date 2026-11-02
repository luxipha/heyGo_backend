package agent

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Explicit opt-in smoke test. Ordinary CI never calls the donated public API.
func TestNominatimLiveExplicitSearch(t *testing.T) {
	if os.Getenv("NOMINATIM_LIVE_SMOKE") != "true" {
		t.Skip("public lookup requires explicit opt-in")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("live lookup requires the application's shared database budget")
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p := NominatimPlaces{BaseURL: "https://nominatim.openstreetmap.org", UserAgent: "HeyGo/1.0 (+https://heygo.ng)", Store: PostgresNominatimStore{Pool: pool}}
	places, err := p.Search(context.Background(), "Lekki, Lagos", nil)
	if err != nil || len(places) == 0 {
		t.Fatalf("public place lookup unavailable: %v", err)
	}
	// Selection must use the cached result and spend no second upstream request.
	place, err := p.Resolve(context.Background(), places[0].ID)
	if err != nil || place.ID != places[0].ID {
		t.Fatalf("selection failed: %v", err)
	}
	t.Logf("Public lookup returned %d selectable Nigerian places; cached selection passed", len(places))
}

type memoryNominatimStore struct {
	data                   map[string][]Place
	reservations, backoffs int
	busy, broken           bool
}

func (s *memoryNominatimStore) Read(_ context.Context, key string) ([]Place, bool, error) {
	if s.broken {
		return nil, false, errors.New("cache offline")
	}
	p, ok := s.data[key]
	return p, ok, nil
}
func (s *memoryNominatimStore) Write(_ context.Context, key string, p []Place) error {
	s.data[key] = p
	return nil
}
func (s *memoryNominatimStore) Reserve(context.Context, string) error {
	s.reservations++
	if s.busy {
		return errors.New("busy")
	}
	return nil
}
func (s *memoryNominatimStore) Backoff(context.Context, string) error {
	s.backoffs++
	s.busy = true
	return nil
}
func memoryPlaces() *memoryNominatimStore { return &memoryNominatimStore{data: map[string][]Place{}} }

const nominatimFixture = `[{"osm_type":"way","osm_id":123,"name":"Lekki","display_name":"Lekki, Lagos, Nigeria","lat":"6.45","lon":"3.45"},{"osm_type":"way","osm_id":123,"name":"Duplicate","lat":"6.45","lon":"3.45"},{"osm_type":"node","osm_id":456,"name":"Outside Nigeria","lat":"51","lon":"0"}]`

func TestNominatimSearchAndSelectionUseSharedCache(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "GET" || r.URL.Path != "/search" {
			t.Errorf("unexpected upstream operation %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("q") != "Lekki" || q.Get("countrycodes") != "ng" || q.Get("format") != "jsonv2" || q.Get("limit") != "5" || q.Get("viewbox") == "" {
			t.Errorf("invalid search contract %v", q)
		}
		if r.Header.Get("User-Agent") != "HeyGo-test (+https://heygo.ng)" || r.Header.Get("Authorization") != "" {
			t.Error("identification missing or private credentials forwarded")
		}
		w.Write([]byte(nominatimFixture))
	}))
	defer server.Close()
	store := memoryPlaces()
	p := NominatimPlaces{BaseURL: server.URL, UserAgent: "HeyGo-test (+https://heygo.ng)", Client: server.Client(), Store: store}
	for i := 0; i < 2; i++ {
		places, err := p.Search(context.Background(), "  Lekki  ", nil)
		if err != nil || len(places) != 1 || places[0].ID != "osm_W_123" || places[0].Provider != "nominatim" {
			t.Fatalf("bad normalized places %+v %v", places, err)
		}
	}
	resolved, err := p.Resolve(context.Background(), "osm_W_123")
	if err != nil || resolved.Name != "Lekki" || hits != 1 || store.reservations != 1 {
		t.Fatalf("selection performed an unnecessary upstream lookup: hits=%d reservations=%d err=%v", hits, store.reservations, err)
	}
}

func TestNominatimHostedSwitchRetainsOSMIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hosted/lookup" || r.URL.Query().Get("osm_ids") != "W123" {
			t.Errorf("incorrect lookup %s", r.URL)
		}
		w.Write([]byte(nominatimFixture))
	}))
	defer server.Close()
	p := NominatimPlaces{BaseURL: server.URL + "/hosted", Client: server.Client(), Store: memoryPlaces()}
	place, err := p.Resolve(context.Background(), "osm_W_123")
	if err != nil || place.ID != "osm_W_123" {
		t.Fatalf("reference failed after hosting switch %v", err)
	}
}

func TestNominatimRefusesRequestsWithoutSharedBudgetOrCache(t *testing.T) {
	for _, store := range []*memoryNominatimStore{nil, {data: map[string][]Place{}, busy: true}, {broken: true}} {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
		p := NominatimPlaces{BaseURL: server.URL, Client: server.Client()}
		if store != nil {
			p.Store = store
		}
		_, err := p.Search(context.Background(), "Lekki", nil)
		server.Close()
		if err == nil || hits != 0 {
			t.Fatal("unbudgeted request escaped")
		}
	}
}

func TestNominatimProviderRefusalBacksOffAndMalformedResponsesFail(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		backoff bool
	}{{429, "", true}, {403, "", true}, {200, nominatimFixture + ` {}`, false}, {200, `{"bad":"response"}`, false}, {500, "", false}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
		store := memoryPlaces()
		p := NominatimPlaces{BaseURL: server.URL, Client: server.Client(), Store: store}
		_, err := p.Search(context.Background(), "Lekki", nil)
		server.Close()
		if err == nil || (store.backoffs > 0) != tc.backoff || len(store.data) != 0 {
			t.Fatalf("provider failure cached or ignored: %+v %v", tc, err)
		}
	}
}

func TestNominatimRejectsInvalidReferenceAndSensitiveSearch(t *testing.T) {
	p := NominatimPlaces{Store: memoryPlaces()}
	for _, id := range []string{"google_id", "osm_N_0", "osm_W_12/../details", "osm_R_99999999999999999999999999"} {
		if _, err := p.Resolve(context.Background(), id); err == nil {
			t.Fatalf("bad ID accepted %s", id)
		}
	}
	for _, q := range []string{"ab", strings.Repeat("a", 161), "email a@example.com", "OTP 12345678"} {
		if _, err := p.Search(context.Background(), q, nil); err == nil {
			t.Fatal("bad query accepted")
		}
	}
}

func TestNominatimConfigurationAndHostedEndpoint(t *testing.T) {
	t.Setenv("GEO_PLACE_PROVIDER", "nominatim")
	t.Setenv("NOMINATIM_BASE_URL", "")
	p, err := ConfiguredPlaces()
	if err != nil || p.(NominatimPlaces).BaseURL != "https://nominatim.openstreetmap.org" {
		t.Fatal("public default missing")
	}
	for _, endpoint := range []string{"http://nominatim.openstreetmap.org", "https://user:secret@host.example", "https://host.example?key=private", "https://nominatim.openstreetmap.org/search"} {
		t.Setenv("NOMINATIM_BASE_URL", endpoint)
		if _, err := ConfiguredPlaces(); err == nil {
			t.Fatalf("unsafe endpoint accepted %s", endpoint)
		}
	}
	t.Setenv("NOMINATIM_BASE_URL", "https://geocode.heygo.ng/nominatim/")
	p, err = ConfiguredPlaces()
	if err != nil || p.(NominatimPlaces).BaseURL != "https://geocode.heygo.ng/nominatim" {
		t.Fatal("hosted migration requires more than endpoint change")
	}
}
