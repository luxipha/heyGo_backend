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

// TurnContext is UI context, never authorization or proof of a valid quote.
// The existing authenticated preview/booking APIs remain authoritative.
type TurnContext struct {
	HasDestination bool `json:"hasDestination"`
	HasQuote       bool `json:"hasQuote"`
}
type TurnPlanner interface {
	PlanTurn(context.Context, string, TurnContext) (Plan, error)
}
type Request struct {
	Autocomplete   bool   `json:"autocomplete,omitempty"`
	SessionToken   string `json:"sessionToken,omitempty"`
	NoPublicPlaces bool   `json:"noPublicPlaces,omitempty"`

	SchemaVersion int          `json:"schemaVersion"`
	Message       string       `json:"message"`
	PlaceID       string       `json:"placeId"`
	Bias          *Coordinate  `json:"bias,omitempty"`
	Context       *TurnContext `json:"context,omitempty"`
	Guided        bool         `json:"guided,omitempty"`
}
type Response struct {
	Suggestions []Suggestion `json:"suggestions,omitempty"`

	SchemaVersion        int     `json:"schemaVersion"`
	State                string  `json:"state"`
	Message              string  `json:"message"`
	Candidates           []Place `json:"candidates"`
	Place                *Place  `json:"place"`
	Planner              string  `json:"planner"`
	ConfirmationRequired bool    `json:"confirmationRequired"`
	Action               string  `json:"action,omitempty"`
	SavedPlaceLabel      string  `json:"savedPlaceLabel,omitempty"`
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
var searchSession = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func ValidLocation(p Coordinate) bool {
	return p.Latitude >= 4 && p.Latitude <= 14 && p.Longitude >= 2 && p.Longitude <= 15
}
func ValidatePlan(plan Plan) error {
	if plan.Tool == "resolve_saved_place" && (plan.Query == "Home" || plan.Query == "Work") {
		return nil
	}
	if (plan.Tool == "clarify_destination" || plan.Tool == "preview_ride" || plan.Tool == "request_ride") && plan.Query == "" {
		return nil
	}
	if plan.Tool != "search_places" || len(plan.Query) > 160 || strings.TrimSpace(plan.Query) == "" || sensitive.MatchString(plan.Query) {
		return errors.New("invalid ride tool")
	}
	return nil
}

// Strip only explicit ride-to phrasing; sensitive destinations/actions still
// pass through the existing guard. Bare account/dispatch commands stay blocked.
func normalizeDestinationMessage(message string) string {
	query := strings.TrimSpace(message)
	lower := strings.ToLower(query)
	for _, prefix := range []string{"book me a ride to ", "book a ride to ", "order me a ride to ", "order a ride to ", "get me a ride to ", "i need a ride to ", "can you take me to "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(query[len(prefix):])
		}
	}
	return query
}

var contextualBooking = regexp.MustCompile(`(?i)^(please\s+)?(book|order|request)\s+(it|this( ride| one)?|that( ride| one)?)( please)?[.!?]*$`)
var greeting = regexp.MustCompile(`(?i)^(hi|hello|hey|good morning|good afternoon|good evening|thanks|thank you)[.!?]*$`)

func Deterministic(message string) (Plan, error) {
	query := normalizeDestinationMessage(message)
	lower := strings.ToLower(query)
	if greeting.MatchString(query) {
		return Plan{Tool: "clarify_destination"}, nil
	}
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
	if request.NoPublicPlaces {
		ctx = context.WithValue(ctx, noPublicPlacesKey{}, true)
	}
	if request.SessionToken != "" && !searchSession.MatchString(request.SessionToken) {
		return reply, errors.New("invalid search session")
	}
	if request.Autocomplete {
		if request.PlaceID != "" || !validSearch(request.Message, request.Bias) {
			return reply, errors.New("invalid autocomplete request")
		}
		provider, ok := g.Places.(AutocompleteProvider)
		if !ok {
			return reply, errors.New("autocomplete unavailable")
		}
		suggestions, err := provider.Suggest(ctx, request.Message, request.Bias, request.SessionToken)
		if err != nil {
			return reply, err
		}
		reply.State = "suggestions"
		reply.Message = "Select an address to resolve its coordinates."
		for _, s := range suggestions {
			if placeID.MatchString(s.ID) && s.Name != "" && len(s.Name) <= 256 && len(s.Address) <= 1024 {
				reply.Suggestions = append(reply.Suggestions, s)
				if len(reply.Suggestions) == 5 {
					break
				}
			}
		}
		return reply, nil
	}
	var candidates []Place
	if request.PlaceID != "" {
		if !placeID.MatchString(request.PlaceID) {
			return reply, errors.New("invalid place id")
		}
		var place Place
		var err error
		if resolver, ok := g.Places.(SessionResolver); ok {
			place, err = resolver.ResolveSession(ctx, request.PlaceID, request.SessionToken)
		} else {
			place, err = g.Places.Resolve(ctx, request.PlaceID)
		}
		if err != nil {
			return reply, err
		}
		if place.ID != request.PlaceID {
			return reply, errors.New("invalid resolved place")
		}
		candidates = []Place{place}
	} else {
		// Sensitive/unsupported intents never leave HeyGo or reach the LLM.
		message := normalizeDestinationMessage(request.Message)
		if request.Context != nil && request.Context.HasDestination && contextualBooking.MatchString(message) {
			message = "request ride"
		}
		if sensitive.MatchString(message) {
			reply.State = "unsupported"
			reply.Message = "I can help find a ride destination. Confirm trip actions using the ride controls."
			return reply, nil
		}
		plan, err := Deterministic(message)
		if err != nil {
			reply.State = "clarification"
			reply.Message = "Enter a destination name or address."
			return reply, nil
		}
		if plan.Tool != "clarify_destination" && !request.Guided && g.LLMEnabled && g.Planner != nil {
			var proposed Plan
			var plannerErr error
			if planner, ok := g.Planner.(TurnPlanner); ok && request.Context != nil {
				proposed, plannerErr = planner.PlanTurn(ctx, plan.Query, *request.Context)
			} else {
				proposed, plannerErr = g.Planner.Plan(ctx, plan.Query)
			}
			if plannerErr == nil && ValidatePlan(proposed) == nil {
				plan = proposed
				reply.Planner = "llm"
			}
		}
		// An unavailable interpreter must not replace a validated route with a
		// Places search for a contextual reply such as "could I see the price?".
		// Guided searches deliberately bypass this conversational branch.
		if request.Context != nil && request.Context.HasDestination && !request.Guided && reply.Planner != "llm" && plan.Tool != "clarify_destination" {
			reply.State = "clarification"
			reply.Message = "Use the ride controls to review your fare, or guided search to change destination."
			return reply, nil
		}
		if plan.Tool == "resolve_saved_place" {
			if request.Context != nil {
				reply.State = "action"
				reply.Action = plan.Tool
				reply.SavedPlaceLabel = plan.Query
				reply.Message = "Review your saved destination and pickup."
				return reply, nil
			}
			plan = Plan{Tool: "clarify_destination"}
		}
		if plan.Tool == "preview_ride" || plan.Tool == "request_ride" {
			if request.Context != nil && request.Context.HasDestination && (plan.Tool != "request_ride" || request.Context.HasQuote) {
				reply.State = "action"
				reply.Action = plan.Tool
				reply.Message = "Review the ride details using the ride controls."
				return reply, nil
			}
			plan = Plan{Tool: "clarify_destination"}
		}
		if plan.Tool == "clarify_destination" {
			reply.State = "clarification"
			reply.Message = "Where would you like to go? Enter a destination or choose a saved place."
			return reply, nil
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
	mask := "id,formattedAddress,location"
	if path == "/places:searchText" {
		mask = "places.id,places.displayName,places.formattedAddress,places.location"
	}
	if path == "/places:autocomplete" {
		mask = "suggestions.placePrediction.placeId,suggestions.placePrediction.text.text,suggestions.placePrediction.structuredFormat"
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
	name := p.DisplayName.Text
	if name == "" {
		name = strings.TrimSpace(strings.Split(p.Address, ",")[0])
	}
	return Place{ID: p.ID, Name: name, Address: p.Address, Location: p.Location, Provider: "google"}
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
	return p.ResolveSession(ctx, id, "")
}
func (p GooglePlaces) ResolveSession(ctx context.Context, id, token string) (Place, error) {
	if !placeID.MatchString(id) {
		return Place{}, errors.New("invalid place id")
	}
	path := "/places/" + url.PathEscape(id)
	if token != "" {
		path += "?sessionToken=" + url.QueryEscape(token)
	}
	raw, err := p.call(ctx, "GET", path, nil)
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
