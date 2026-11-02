package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fallbackPlaces struct {
	calls int
	err   error
	empty bool
	name  string
}

func (p *fallbackPlaces) Search(context.Context, string, *Coordinate) ([]Place, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.empty {
		return []Place{}, nil
	}
	return []Place{{ID: "some-place", Name: "Lekki", Provider: p.name, Location: Coordinate{6.5, 3.4}}}, nil
}
func (p *fallbackPlaces) Resolve(context.Context, string) (Place, error) {
	return Place{}, errors.New("not used")
}
func (p *fallbackPlaces) Suggest(context.Context, string, *Coordinate, string) ([]Suggestion, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.empty {
		return []Suggestion{}, nil
	}
	return []Suggestion{{ID: "some-place", Name: "Lekki", Provider: p.name}}, nil
}
func TestFallbackOrderAndPublicAutocompleteExclusion(t *testing.T) {
	first := &fallbackPlaces{err: errors.New("timeout")}
	second := &fallbackPlaces{name: "geoapify"}
	public := &fallbackPlaces{name: "nominatim"}
	chain := PlaceChain{[]NamedPlaces{{"google", first}, {"geoapify", second}, {"nominatim", public}}}
	out, err := chain.Suggest(context.Background(), "Lekki Lagos", nil, "session-token-12345")
	if err != nil || len(out) != 1 || first.calls != 1 || second.calls != 1 || public.calls != 0 {
		t.Fatal("fallback order failed")
	}
	second.err = errors.New("quota")
	_, err = chain.Suggest(context.Background(), "Lekki Lagos", nil, "")
	if err == nil || public.calls != 0 {
		t.Fatal("public Nominatim received autocomplete")
	}
	second.empty = true
	second.err = nil
	places, err := chain.Search(context.Background(), "Lekki Lagos", nil)
	if err != nil || len(places) != 1 || public.calls != 1 {
		t.Fatal("explicit fallback failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := first.calls
	_, _ = chain.Suggest(ctx, "Lekki Lagos", nil, "")
	if first.calls != before {
		t.Fatal("cancelled request reached upstream")
	}
	ctx = context.WithValue(context.Background(), noPublicPlacesKey{}, true)
	_, _ = chain.Search(ctx, "Lekki Lagos", nil)
	if public.calls != 1 {
		t.Fatal("generated request reached public provider")
	}
}
func TestGoogleAutocompleteCoordinatesOnlyOnSelection(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Goog-Api-Key") != "synthetic" {
			t.Error("missing private header")
		}
		if r.URL.Path == "/places:autocomplete" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sessionToken"] != "session-token-12345" || body["input"] != "Lekki Lagos" {
				t.Error("session/input lost")
			}
			if strings.Contains(r.Header.Get("X-Goog-FieldMask"), "location") {
				t.Error("suggestions fetched coordinates")
			}
			w.Write([]byte(`{"suggestions":[{"placePrediction":{"placeId":"google-place","text":{"text":"Lekki, Lagos"},"structuredFormat":{"mainText":{"text":"Lekki"}}}}]}`))
			return
		}
		if r.Method != "GET" || r.URL.Query().Get("sessionToken") != "session-token-12345" || r.Header.Get("X-Goog-FieldMask") != "id,formattedAddress,location" {
			t.Error("selection did not use Essentials session")
		}
		w.Write([]byte(`{"id":"google-place","formattedAddress":"Lekki, Lagos","location":{"latitude":6.45,"longitude":3.4}}`))
	}))
	defer server.Close()
	google := GooglePlaces{Key: "synthetic", Client: server.Client(), BaseURL: server.URL}
	rows, err := google.Suggest(context.Background(), "Lekki Lagos", nil, "session-token-12345")
	if err != nil || len(rows) != 1 || calls != 1 {
		t.Fatal("autocomplete failed", err)
	}
	place, err := google.ResolveSession(context.Background(), rows[0].ID, "session-token-12345")
	if err != nil || place.Name != "Lekki" || calls != 2 {
		t.Fatal("resolve failed", err)
	}
}
func TestGeoapifyNormalizationAndReferenceResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apiKey") != "synthetic" {
			t.Error("missing key")
		}
		if strings.Contains(r.URL.Path, "autocomplete") && r.URL.Query().Get("filter") != "countrycode:ng" {
			t.Error("Nigeria restriction lost")
		}
		if strings.Contains(r.URL.Path, "place-details") && r.URL.Query().Get("id") != "abc123" {
			t.Error("provider reference lost")
		}
		id := "abc123"
		if strings.Contains(r.URL.Path, "place-details") {
			id = "canonical456"
		}
		payload := `{"features":[{"properties":{"place_id":"` + id + `","address_line1":"Lekki","formatted":"Lekki, Lagos","lat":6.45,"lon":3.4}},{"properties":{"place_id":"outside","name":"Outside","lat":51,"lon":0}}]}`
		w.Write([]byte(payload))
	}))
	defer server.Close()
	p := GeoapifyPlaces{Key: "synthetic", Client: server.Client(), BaseURL: server.URL}
	rows, err := p.Suggest(context.Background(), "Lekki Lagos", nil, "")
	if err != nil || len(rows) != 1 || rows[0].ID != "geoapify_abc123" {
		t.Fatal("normalization failed", err)
	}
	place, err := p.Resolve(context.Background(), rows[0].ID)
	if err != nil || place.ID != rows[0].ID || place.Location.Latitude != 6.45 {
		t.Fatal("selection changed coordinates", err)
	}
	if _, err = p.Resolve(context.Background(), "https://example.com/key"); err == nil {
		t.Fatal("arbitrary reference accepted")
	}
}
func TestSuggestionsNeverResolveOrInvokePlanner(t *testing.T) {
	p := &fallbackPlaces{name: "google"}
	g := Gateway{Places: PlaceChainProvider{PlaceChain{[]NamedPlaces{{"google", p}}}}}
	response, err := g.Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Lekki Lagos", Autocomplete: true})
	if err != nil || response.State != "suggestions" || response.Place != nil || len(response.Candidates) != 0 || !response.ConfirmationRequired {
		t.Fatal("suggestions promoted to destination", err)
	}
}

// Test-only provider delegates search and suggestions; resolution must not run.
type PlaceChainProvider struct{ PlaceChain }

func (p PlaceChainProvider) Resolve(context.Context, string) (Place, error) {
	panic("suggestions must not resolve")
}

func TestGeoapifyCachedSelectionAndEndpointIsolation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"features":[{"properties":{"place_id":"abc123","name":"Lekki","formatted":"Lekki Lagos","lat":6.45,"lon":3.4}}]}`))
	}))
	defer server.Close()
	store := &memoryNominatimStore{data: map[string][]Place{}}
	p := GeoapifyPlaces{Key: "synthetic", BaseURL: server.URL, Client: server.Client(), Store: store}
	suggestions, err := p.Suggest(context.Background(), "Lekki Lagos", nil, "")
	if err != nil || len(suggestions) != 1 {
		t.Fatal(err)
	}
	place, err := p.Resolve(context.Background(), suggestions[0].ID)
	if err != nil || calls != 1 || place.ID != suggestions[0].ID || place.Location.Latitude != 6.45 {
		t.Fatal("selection did not reuse validated coordinates", err)
	}
	other := p
	other.BaseURL = "https://other.example"
	if p.referenceKey(place.ID) == other.referenceKey(place.ID) {
		t.Fatal("cache crosses provider endpoint")
	}
}
