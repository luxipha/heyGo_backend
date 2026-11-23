package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/env"
)

// venueSubmission is what a Hero provides. Photos are base64 data URIs.
type venueSubmission struct {
	HeroID            string   `json:"heroId"`
	HeroName          string   `json:"heroName"`
	ExternalID        string   `json:"externalId"`
	VenueName         string   `json:"venueName"`
	Address           string   `json:"address"`
	City              string   `json:"city,omitempty"`
	Phone             string   `json:"phone"`
	Category          string   `json:"category,omitempty"`
	OwnerName         string   `json:"ownerName"`
	OwnerRole         string   `json:"ownerRole,omitempty"`
	Landmark          string   `json:"landmark,omitempty"`
	LocationAccuracyM float64  `json:"locationAccuracyM,omitempty"`
	KitchenType       string   `json:"kitchenType,omitempty"`
	Sells             []string `json:"sells,omitempty"`
	SocialHandle      string   `json:"socialHandle,omitempty"`
	MenuPhoto         string   `json:"menuPhoto,omitempty"`
	// OwnerConfirmed is true when the owner read a code (sent to their own phone or email) to the Hero.
	OwnerConfirmed bool     `json:"ownerConfirmed"`
	OwnerPhone     string   `json:"ownerPhone,omitempty"`
	OwnerEmail     string   `json:"ownerEmail,omitempty"`
	Notes          string   `json:"notes,omitempty"`
	Photos         []string `json:"photos"`
	Location       *struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"location,omitempty"`
}

// venueReview is the partner platform's view of a listed venue's progress.
// InviteLink is only present right after listing or re-inviting.
type venueReview struct {
	ID         string  `json:"id"`
	VenueName  string  `json:"venueName"`
	Stage      string  `json:"stage"`
	Note       *string `json:"note"`
	InviteLink *string `json:"inviteLink"`
}

// venueDuplicate says why a venue cannot be listed again: it is already on heyGo, another Hero
// holds it for a few days, or this Hero already listed it.
type venueDuplicate struct {
	State    string `json:"state"`
	DaysAgo  int    `json:"daysAgo"`
	DaysLeft int    `json:"daysLeft"`
}

// venuePartnerError carries the partner platform's verdict without leaking its internals.
type venuePartnerError struct {
	Status    int
	Message   string
	Duplicate *venueDuplicate
}

func (e *venuePartnerError) Error() string { return e.Message }

var errVenuePartnerUnavailable = errors.New("venue partner unavailable")

type venuePartner interface {
	Submit(ctx context.Context, s venueSubmission) (venueReview, error)
	Statuses(ctx context.Context, heroID string) (map[string]venueReview, error)
	Reinvite(ctx context.Context, heroID, submissionID string) (venueReview, error)
	Check(ctx context.Context, heroID, venueName string, lat, lng float64, hasLocation bool) (venueDuplicate, error)
}

type httpVenuePartner struct {
	base   string
	key    string
	client *http.Client
}

func newVenuePartnerFromEnv() venuePartner {
	base := strings.TrimRight(env.GetString("VENUE_PARTNER_URL", ""), "/")
	key := env.GetString("VENUE_PARTNER_SERVICE_KEY", "")
	if base == "" || len(key) < 32 {
		return nil
	}
	return &httpVenuePartner{base: base, key: key, client: &http.Client{Timeout: 25 * time.Second}}
}

func (p *httpVenuePartner) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-heygo-service-key", p.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, errVenuePartnerUnavailable
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusServiceUnavailable {
		return resp.StatusCode, errVenuePartnerUnavailable
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error     string          `json:"error"`
			Issues    []string        `json:"issues"`
			Duplicate *venueDuplicate `json:"duplicate"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if len(e.Issues) > 0 {
			msg = strings.Join(e.Issues, ". ")
		}
		return resp.StatusCode, &venuePartnerError{Status: resp.StatusCode, Message: msg, Duplicate: e.Duplicate}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, errVenuePartnerUnavailable
		}
	}
	return resp.StatusCode, nil
}

func (p *httpVenuePartner) Submit(ctx context.Context, s venueSubmission) (venueReview, error) {
	var out struct {
		Submission venueReview `json:"submission"`
	}
	_, err := p.do(ctx, http.MethodPost, "/api/heygo/venue-submissions", s, &out)
	return out.Submission, err
}

func (p *httpVenuePartner) Statuses(ctx context.Context, heroID string) (map[string]venueReview, error) {
	var out struct {
		Submissions []venueReview `json:"submissions"`
	}
	_, err := p.do(ctx, http.MethodGet, "/api/heygo/venue-submissions?heroId="+url.QueryEscape(heroID), nil, &out)
	if err != nil {
		return nil, err
	}
	m := make(map[string]venueReview, len(out.Submissions))
	for _, s := range out.Submissions {
		m[s.ID] = s
	}
	return m, nil
}

func (p *httpVenuePartner) Reinvite(ctx context.Context, heroID, submissionID string) (venueReview, error) {
	var out struct {
		Submission venueReview `json:"submission"`
	}
	_, err := p.do(ctx, http.MethodPost, "/api/heygo/venue-submissions/"+url.PathEscape(submissionID)+"/resend", map[string]string{"heroId": heroID}, &out)
	return out.Submission, err
}

func (p *httpVenuePartner) Check(ctx context.Context, heroID, venueName string, lat, lng float64, hasLocation bool) (venueDuplicate, error) {
	body := map[string]any{"venueName": venueName, "heroId": heroID}
	if hasLocation {
		body["location"] = map[string]float64{"lat": lat, "lng": lng}
	}
	var out venueDuplicate
	_, err := p.do(ctx, http.MethodPost, "/api/heygo/venue-check", body, &out)
	return out, err
}
