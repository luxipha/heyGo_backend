// Package agent resolves ride destinations. It cannot price, dispatch, cancel,
// modify accounts, or operate on payment instruments.
package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Coordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type Place struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Address  string     `json:"address"`
	Location Coordinate `json:"location"`
	Provider string     `json:"provider"`
}
type Request struct {
	SchemaVersion int         `json:"schemaVersion"`
	Message       string      `json:"message"`
	PlaceID       string      `json:"placeId"`
	Bias          *Coordinate `json:"bias,omitempty"`
}
type Response struct {
	SchemaVersion        int     `json:"schemaVersion"`
	State                string  `json:"state"`
	Message              string  `json:"message"`
	Candidates           []Place `json:"candidates"`
	Place                *Place  `json:"place"`
	Planner              string  `json:"planner"`
	ConfirmationRequired bool    `json:"confirmationRequired"`
}
type Provider interface {
	Search(context.Context, string, *Coordinate) ([]Place, error)
	Resolve(context.Context, string) (Place, error)
}
type Plan struct {
	Tool  string `json:"tool"`
	Query string `json:"query"`
}
type Planner interface {
	Plan(context.Context, string) (Plan, error)
}
type Gateway struct {
	Places        Provider
	Planner       Planner
	LLMEnabled    bool
	InternalToken string
}

var sensitive = regexp.MustCompile(`(?i)(wallet|credit.card|payment|balance|refund|otp|password|phone|email|account|privacy|delete|export|admin|support|cancel|book|dispatch|api.key|secret|[0-9]{7,}|@)`)
var placeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func ValidLocation(p Coordinate) bool {
	return p.Latitude >= 4 && p.Latitude <= 14 && p.Longitude >= 2 && p.Longitude <= 15
}
func ValidatePlan(plan Plan) error {
	if plan.Tool != "search_places" || len(plan.Query) > 160 || strings.TrimSpace(plan.Query) == "" || sensitive.MatchString(plan.Query) {
		return errors.New("invalid ride tool")
	}
	return nil
}
func Deterministic(message string) (Plan, error) {
	query := strings.TrimSpace(message)
	lower := strings.ToLower(query)
	for _, prefix := range []string{"take me to ", "i want to go to ", "go to ", "drive me to "} {
		if strings.HasPrefix(lower, prefix) {
			query = strings.TrimSpace(query[len(prefix):])
			break
		}
	}
	if strings.EqualFold(query, "take me home") || strings.EqualFold(query, "home") {
		return Plan{}, errors.New("saved destination unavailable")
	}
	plan := Plan{Tool: "search_places", Query: query}
	return plan, ValidatePlan(plan)
}
func (g *Gateway) Resolve(ctx context.Context, request Request) (Response, error) {
	reply := Response{SchemaVersion: 1, Candidates: []Place{}, Planner: "deterministic", ConfirmationRequired: true}
	if request.SchemaVersion != 1 || len(request.Message) > 500 || (request.Bias != nil && !ValidLocation(*request.Bias)) {
		return reply, errors.New("invalid request")
	}
	if g.Places == nil {
		return reply, errors.New("places unavailable")
	}
	var candidates []Place
	if request.PlaceID != "" {
		if !placeID.MatchString(request.PlaceID) {
			return reply, errors.New("invalid place id")
		}
		place, err := g.Places.Resolve(ctx, request.PlaceID)
		if err != nil {
			return reply, err
		}
		if place.ID != request.PlaceID {
			return reply, errors.New("invalid resolved place")
		}
		candidates = []Place{place}
	} else {
		// Sensitive/unsupported intents never leave HeyGo or reach the LLM.
		if sensitive.MatchString(request.Message) {
			reply.State = "unsupported"
			reply.Message = "I can help find a ride destination. Confirm trip actions using the ride controls."
			return reply, nil
		}
		plan, err := Deterministic(request.Message)
		if err != nil {
			reply.State = "clarification"
			reply.Message = "Enter a destination name or address."
			return reply, nil
		}
		if g.LLMEnabled && g.Planner != nil {
			if proposed, err := g.Planner.Plan(ctx, plan.Query); err == nil && ValidatePlan(proposed) == nil {
				plan = proposed
				reply.Planner = "llm"
			}
		}
		candidates, err = g.Places.Search(ctx, plan.Query, request.Bias)
		if err != nil {
			return reply, err
		}
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		if placeID.MatchString(p.ID) && p.Name != "" && len(p.Name) <= 256 && len(p.Address) <= 1024 && ValidLocation(p.Location) && !seen[p.ID] {
			reply.Candidates = append(reply.Candidates, p)
			seen[p.ID] = true
			if len(reply.Candidates) == 5 {
				break
			}
		}
	}
	switch len(reply.Candidates) {
	case 0:
		reply.State = "unavailable"
		reply.Message = "I couldn’t resolve that destination. Try a more specific address."
	case 1:
		reply.State = "resolved"
		reply.Place = &reply.Candidates[0]
		reply.Message = "Review this destination before confirming the route."
	default:
		reply.State = "clarification"
		reply.Message = "Which destination do you mean?"
	}
	return reply, nil
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path != "/v1/resolve" || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	if g.InternalToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.InternalToken)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	var request Request
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	response, err := g.Resolve(r.Context(), request)
	if err != nil {
		http.Error(w, "place resolution unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(response)
}

type GooglePlaces struct {
	Key     string
	Client  *http.Client
	BaseURL string
}

func (p GooglePlaces) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	if p.Key == "" {
		return nil, errors.New("places not configured")
	}
	base := p.BaseURL
	if base == "" {
		base = "https://places.googleapis.com/v1"
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", p.Key)
	mask := "id,displayName,formattedAddress,location"
	if method == "POST" {
		mask = "places.id,places.displayName,places.formattedAddress,places.location"
	}
	req.Header.Set("X-Goog-FieldMask", mask)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("places provider unavailable")
	}
	return io.ReadAll(io.LimitReader(res.Body, 256<<10))
}

type googlePlace struct {
	ID          string `json:"id"`
	DisplayName struct {
		Text string `json:"text"`
	} `json:"displayName"`
	Address  string     `json:"formattedAddress"`
	Location Coordinate `json:"location"`
}

func (p googlePlace) public() Place {
	return Place{ID: p.ID, Name: p.DisplayName.Text, Address: p.Address, Location: p.Location, Provider: "google"}
}
func (p GooglePlaces) Search(ctx context.Context, query string, bias *Coordinate) ([]Place, error) {
	body := map[string]any{"textQuery": query, "regionCode": "NG", "languageCode": "en", "pageSize": 5}
	if bias != nil {
		body["locationBias"] = map[string]any{"circle": map[string]any{"center": bias, "radius": 15000}}
	}
	raw, err := p.call(ctx, "POST", "/places:searchText", body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Places []googlePlace `json:"places"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	places := make([]Place, 0, len(result.Places))
	for _, item := range result.Places {
		places = append(places, item.public())
	}
	return places, nil
}
func (p GooglePlaces) Resolve(ctx context.Context, id string) (Place, error) {
	if !placeID.MatchString(id) {
		return Place{}, errors.New("invalid place id")
	}
	raw, err := p.call(ctx, "GET", "/places/"+url.PathEscape(id), nil)
	if err != nil {
		return Place{}, err
	}
	var result googlePlace
	err = json.Unmarshal(raw, &result)
	return result.public(), err
}

// HTTPPlanner is a private planner adapter. Only a sanitized place query is
// sent: no user IDs, tokens, account data, routes, prices or event telemetry.
type HTTPPlanner struct {
	URL, Token string
	Client     *http.Client
}

func (p HTTPPlanner) Plan(ctx context.Context, query string) (Plan, error) {
	if p.URL == "" {
		return Plan{}, errors.New("planner unavailable")
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "query": query, "allowedTools": []string{"search_places"}})
	req, err := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(raw))
	if err != nil {
		return Plan{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return Plan{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Plan{}, errors.New("planner unavailable")
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 2048))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Plan{}, errors.New("invalid planner response")
	}
	return plan, ValidatePlan(plan)
}
