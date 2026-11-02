package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (p GooglePlaces) Suggest(ctx context.Context, q string, bias *Coordinate, token string) ([]Suggestion, error) {
	if !validSearch(q, bias) {
		return nil, errors.New("invalid place query")
	}
	body := map[string]any{"input": q, "includedRegionCodes": []string{"ng"}, "languageCode": "en", "regionCode": "ng"}
	if token != "" {
		body["sessionToken"] = token
	}
	if bias == nil {
		bias = &Coordinate{6.5244, 3.3792}
	}
	body["locationBias"] = map[string]any{"circle": map[string]any{"center": bias, "radius": 30000}}
	raw, err := p.call(ctx, "POST", "/places:autocomplete", body)
	if err != nil {
		return nil, err
	}
	var response struct {
		Suggestions []struct {
			Prediction struct {
				ID   string `json:"placeId"`
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
				Format struct {
					Main struct {
						Text string `json:"text"`
					} `json:"mainText"`
					Secondary struct {
						Text string `json:"text"`
					} `json:"secondaryText"`
				} `json:"structuredFormat"`
			} `json:"placePrediction"`
		} `json:"suggestions"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("invalid autocomplete response")
	}
	out := []Suggestion{}
	for _, item := range response.Suggestions {
		r := item.Prediction
		name := r.Format.Main.Text
		if name == "" {
			name = r.Text.Text
		}
		if !placeID.MatchString(r.ID) || name == "" || len(name) > 256 || len(r.Text.Text) > 1024 {
			continue
		}
		out = append(out, Suggestion{r.ID, name, r.Text.Text, "google"})
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

// Geoapify stays behind the same search/resolve boundary. Credentials never
// appear in public results. Persisted IDs carry their provider namespace.
type GeoapifyPlaceStore interface {
	Read(context.Context, string) ([]Place, bool, error)
	Write(context.Context, string, []Place) error
}
type GeoapifyPlaces struct {
	Store GeoapifyPlaceStore

	Key, BaseURL string
	Client       *http.Client
}
type geoapifyResult struct {
	ID        string  `json:"place_id"`
	Name      string  `json:"name"`
	Address   string  `json:"formatted"`
	Line1     string  `json:"address_line1"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
}

func (r geoapifyResult) place() (Place, bool) {
	name := r.Name
	if name == "" {
		name = r.Line1
	}
	if name == "" {
		name = strings.TrimSpace(strings.Split(r.Address, ",")[0])
	}
	id := "geoapify_" + r.ID
	point := Coordinate{r.Latitude, r.Longitude}
	if !placeID.MatchString(id) || r.ID == "" || name == "" || len(name) > 256 || len(r.Address) > 1024 || !ValidLocation(point) {
		return Place{}, false
	}
	return Place{id, name, r.Address, point, "geoapify"}, true
}
func (p GeoapifyPlaces) referenceKey(id string) string {
	base := p.BaseURL
	if base == "" {
		base = "https://api.geoapify.com"
	}
	sum := sha256.Sum256([]byte(base + ":" + id))
	return "geoapify:" + hex.EncodeToString(sum[:])
}
func (p GeoapifyPlaces) call(ctx context.Context, path string, values url.Values) ([]Place, error) {
	if p.Key == "" {
		return nil, errors.New("Geoapify unavailable")
	}
	base := p.BaseURL
	if base == "" {
		base = "https://api.geoapify.com"
	}
	values.Set("apiKey", p.Key)
	req, err := http.NewRequestWithContext(ctx, "GET", base+path+"?"+values.Encode(), nil)
	if err != nil {
		return nil, errors.New("Geoapify unavailable")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Geoapify unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("Geoapify unavailable")
	}
	var result struct {
		Features []struct {
			Properties geoapifyResult `json:"properties"`
		} `json:"features"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 256<<10))
	if decoder.Decode(&result) != nil {
		return nil, errors.New("invalid Geoapify response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid Geoapify response")
	}
	out := []Place{}
	seen := map[string]bool{}
	for _, feature := range result.Features {
		if place, ok := feature.Properties.place(); ok && !seen[place.ID] {
			seen[place.ID] = true
			out = append(out, place)
			if len(out) == 5 {
				break
			}
		}
	}
	if p.Store != nil {
		for _, place := range out {
			_ = p.Store.Write(ctx, p.referenceKey(place.ID), []Place{place})
		}
	}
	return out, nil
}
func (p GeoapifyPlaces) search(ctx context.Context, q string, bias *Coordinate, path string) ([]Place, error) {
	if !validSearch(q, bias) {
		return nil, errors.New("invalid place query")
	}
	if bias == nil {
		bias = &Coordinate{6.5244, 3.3792}
	}
	values := url.Values{"text": {q}, "filter": {"countrycode:ng"}, "bias": {fmt.Sprintf("proximity:%f,%f", bias.Longitude, bias.Latitude)}, "limit": {"5"}, "lang": {"en"}}
	return p.call(ctx, path, values)
}
func (p GeoapifyPlaces) Search(ctx context.Context, q string, bias *Coordinate) ([]Place, error) {
	return p.search(ctx, q, bias, "/v1/geocode/search")
}
func (p GeoapifyPlaces) Suggest(ctx context.Context, q string, bias *Coordinate, _ string) ([]Suggestion, error) {
	places, err := p.search(ctx, q, bias, "/v1/geocode/autocomplete")
	out := []Suggestion{}
	for _, place := range places {
		out = append(out, Suggestion{place.ID, place.Name, place.Address, place.Provider})
	}
	return out, err
}
func (p GeoapifyPlaces) Resolve(ctx context.Context, id string) (Place, error) {
	if !strings.HasPrefix(id, "geoapify_") || !placeID.MatchString(id) {
		return Place{}, errors.New("invalid Geoapify reference")
	}
	if p.Store != nil {
		cached, ok, err := p.Store.Read(ctx, p.referenceKey(id))
		if err == nil && ok && len(cached) == 1 && cached[0].ID == id && ValidLocation(cached[0].Location) {
			return cached[0], nil
		}
	}
	places, err := p.call(ctx, "/v2/place-details", url.Values{"id": {strings.TrimPrefix(id, "geoapify_")}, "features": {"details"}})
	if err != nil {
		return Place{}, err
	}
	// Geoapify may encode a different canonical ID for the same lookup's
	// detailed geometry. Retain the requested reference; never substitute a
	// different provider or accept coordinates supplied by the client.
	if len(places) > 0 {
		place := places[0]
		place.ID = id
		if p.Store != nil {
			_ = p.Store.Write(ctx, p.referenceKey(id), []Place{place})
		}
		return place, nil
	}
	return Place{}, errors.New("Geoapify place unavailable")
}
