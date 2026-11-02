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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Nominatim is an explicit-search adapter. Public OSM does not permit typeahead.
// Use the same adapter with a hosted base URL when moving off the public service.
type NominatimPlaces struct {
	BaseURL, UserAgent string
	Client             *http.Client
	Store              NominatimStore
}

type NominatimStore interface {
	Read(context.Context, string) ([]Place, bool, error)
	Write(context.Context, string, []Place) error
	Reserve(context.Context, string) error
	Backoff(context.Context, string) error
}

var osmReference = regexp.MustCompile(`^osm_([NWR])_([1-9][0-9]{0,18})$`)
var nominatimHTTP = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type nominatimResult struct {
	Type      string `json:"osm_type"`
	ID        int64  `json:"osm_id"`
	Latitude  string `json:"lat"`
	Longitude string `json:"lon"`
	Name      string `json:"name"`
	Address   string `json:"display_name"`
}

func (r nominatimResult) place() (Place, bool) {
	kind := map[string]string{"node": "N", "way": "W", "relation": "R"}[r.Type]
	lat, a := strconv.ParseFloat(r.Latitude, 64)
	lng, b := strconv.ParseFloat(r.Longitude, 64)
	location := Coordinate{lat, lng}
	if kind == "" || r.ID <= 0 || a != nil || b != nil || !ValidLocation(location) {
		return Place{}, false
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = strings.TrimSpace(strings.Split(r.Address, ",")[0])
	}
	if name == "" || len(name) > 256 || len(r.Address) > 1024 {
		return Place{}, false
	}
	return Place{ID: fmt.Sprintf("osm_%s_%d", kind, r.ID), Name: name, Address: r.Address, Location: location, Provider: "nominatim"}, true
}

func (p NominatimPlaces) cacheKey(operation string, values url.Values) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(p.BaseURL, "/") + "/" + operation + "?" + values.Encode()))
	return "nominatim:" + hex.EncodeToString(sum[:])
}

func (p NominatimPlaces) call(ctx context.Context, operation string, values url.Values) ([]Place, error) {
	if p.Store == nil {
		return nil, errors.New("Nominatim shared cache/rate limit unavailable")
	}
	key := p.cacheKey(operation, values)
	cached, ok, err := p.Store.Read(ctx, key)
	if err != nil {
		return nil, err
	}
	if ok {
		return cached, nil
	}
	upstream := strings.TrimRight(p.BaseURL, "/")
	if err = p.Store.Reserve(ctx, upstream); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/"+operation+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en")
	client := p.Client
	if client == nil {
		client = nominatimHTTP
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Nominatim search unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden {
		_ = p.Store.Backoff(ctx, upstream)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Nominatim search unavailable")
	}
	var results []nominatimResult
	decoder := json.NewDecoder(io.LimitReader(response.Body, 256<<10))
	if err = decoder.Decode(&results); err != nil {
		return nil, errors.New("invalid Nominatim response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid Nominatim response")
	}
	places := make([]Place, 0, 5)
	seen := map[string]bool{}
	for _, result := range results {
		if place, valid := result.place(); valid && !seen[place.ID] {
			places = append(places, place)
			seen[place.ID] = true
			if len(places) == 5 {
				break
			}
		}
	}
	if err = p.Store.Write(ctx, key, places); err != nil {
		return nil, err
	}
	for _, place := range places {
		values := url.Values{"format": {"jsonv2"}, "osm_ids": {strings.ReplaceAll(strings.TrimPrefix(place.ID, "osm_"), "_", "")}, "addressdetails": {"1"}}
		if err = p.Store.Write(ctx, p.cacheKey("lookup", values), []Place{place}); err != nil {
			return nil, err
		}
	}
	return places, nil
}

func (p NominatimPlaces) Search(ctx context.Context, query string, bias *Coordinate) ([]Place, error) {
	query = strings.Join(strings.Fields(query), " ")
	if len(query) < 3 || len(query) > 160 || sensitive.MatchString(query) {
		return nil, errors.New("enter a public destination name or address")
	}
	values := url.Values{"q": {query}, "format": {"jsonv2"}, "countrycodes": {"ng"}, "limit": {"5"}, "addressdetails": {"1"}}
	if bias == nil {
		bias = &Coordinate{6.5244, 3.3792}
	}
	if !ValidLocation(*bias) {
		return nil, errors.New("invalid search bias")
	}
	// Prefer the current area without excluding other Nigerian states.
	values.Set("viewbox", fmt.Sprintf("%.2f,%.2f,%.2f,%.2f", bias.Longitude-.3, bias.Latitude+.3, bias.Longitude+.3, bias.Latitude-.3))
	return p.call(ctx, "search", values)
}

func (p NominatimPlaces) Resolve(ctx context.Context, id string) (Place, error) {
	parts := osmReference.FindStringSubmatch(id)
	if parts == nil {
		return Place{}, errors.New("invalid Nominatim reference")
	}
	places, err := p.call(ctx, "lookup", url.Values{"format": {"jsonv2"}, "osm_ids": {parts[1] + parts[2]}, "addressdetails": {"1"}})
	if err != nil {
		return Place{}, err
	}
	for _, place := range places {
		if place.ID == id {
			return place, nil
		}
	}
	return Place{}, errors.New("Nominatim destination unavailable")
}
