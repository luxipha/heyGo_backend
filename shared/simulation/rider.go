package simulation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strings"
	"sync"
)

// RiderAction is deliberately closed: no supplied URL, HTTP method or bearer token.
type RiderAction struct {
	DelaySeconds int    `json:"delaySeconds,omitempty"`
	Available    *bool  `json:"available,omitempty"`
	AutoAccept   *bool  `json:"autoAccept,omitempty"`
	CommandID    string `json:"commandId"`
	Action       string `json:"action"`
	Message      string `json:"message,omitempty"`
	PlaceID      string `json:"placeId,omitempty"`
	FareID       string `json:"fareId,omitempty"`
	TripID       string `json:"tripId,omitempty"`
	Confirmed    bool   `json:"confirmed,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func (b RiderAction) Validate() error {
	if b.DelaySeconds < 0 || b.DelaySeconds > 540 {
		return errors.New("delay must be 0–540 seconds within the ten-minute run")
	}
	if _, e := uuid.Parse(b.CommandID); e != nil {
		return errors.New("valid command ID required")
	}
	switch b.Action {
	case "availability":
		if b.Available == nil {
			return errors.New("availability required")
		}
	case "autoAccept":
		if b.AutoAccept == nil {
			return errors.New("auto-accept preference required")
		}
	case "noShowEligibility", "settlement", "noShowClaim", "settlementConfirm":
		if _, e := uuid.Parse(b.TripID); e != nil {
			return errors.New("valid trip required")
		}
		if (b.Action == "noShowClaim" || b.Action == "settlementConfirm") && !b.Confirmed {
			return errors.New("explicit confirmation required")
		}
	case "search":
		if strings.TrimSpace(b.Message) == "" && b.PlaceID == "" {
			return errors.New("destination text or place ID required")
		}
		if len(b.Message) > 500 || len(b.PlaceID) > 512 {
			return errors.New("destination too long")
		}
	case "preview":
		if b.PlaceID == "" || len(b.PlaceID) > 512 {
			return errors.New("resolved place ID required")
		}
	case "book":
		if _, e := uuid.Parse(b.FareID); e != nil || !b.Confirmed {
			return errors.New("explicit confirmation and valid fare required")
		}
	case "cancel":
		if _, e := uuid.Parse(b.TripID); e != nil || !b.Confirmed || strings.TrimSpace(b.Reason) == "" || len(b.Reason) > 500 {
			return errors.New("confirmed cancellation, trip and reason required")
		}
	case "receipt":
		if _, e := uuid.Parse(b.TripID); e != nil {
			return errors.New("valid trip required")
		}
	default:
		return errors.New("unsupported Rider action")
	}
	return nil
}
func (b RiderAction) OwnerRole() string {
	switch b.Action {
	case "availability", "autoAccept", "noShowEligibility", "noShowClaim", "settlement", "settlementConfirm":
		return "driver"
	}
	return "rider"
}
func (r *Runtime) riderAction(ctx context.Context, a *liveActor, b RiderAction) (map[string]any, error) {
	if e := b.Validate(); e != nil {
		return nil, e
	}
	switch b.Action {
	case "availability":
		return r.request(ctx, a, "POST", "/driver/availability", map[string]bool{"available": *b.Available})
	case "autoAccept":
		prefs, e := r.request(ctx, a, "GET", "/driver/ride-preferences", nil)
		if e != nil {
			return nil, e
		}
		current, ok := prefs["preferences"].(map[string]any)
		if !ok {
			return nil, errors.New("preference response missing")
		}
		current["autoAccept"] = *b.AutoAccept
		delete(current, "updatedAt")
		return r.request(ctx, a, "PUT", "/driver/ride-preferences", current)
	case "noShowEligibility":
		return r.request(ctx, a, "GET", "/driver/trips/"+b.TripID+"/no-show", nil)
	case "noShowClaim":
		return r.requestKey(ctx, a, "POST", "/driver/trips/"+b.TripID+"/no-show-claims", map[string]any{}, b.CommandID)
	case "settlement":
		return r.request(ctx, a, "GET", "/driver/trips/"+b.TripID+"/settlement", nil)
	case "settlementConfirm":
		return r.requestKey(ctx, a, "POST", "/driver/trips/"+b.TripID+"/settlement/confirm", map[string]any{}, b.CommandID)
	case "search":
		return r.request(ctx, a, "POST", "/rider/agent/resolve", map[string]any{"schemaVersion": 1, "guided": true, "noPublicPlaces": true, "message": b.Message, "placeId": b.PlaceID})
	case "preview":
		resolved, e := r.request(ctx, a, "POST", "/rider/agent/resolve", map[string]any{"schemaVersion": 1, "guided": true, "placeId": b.PlaceID})
		if e != nil {
			return nil, e
		}
		place, ok := resolved["place"].(map[string]any)
		if !ok {
			return nil, errors.New("destination requires guided selection")
		}
		preview, e := r.request(ctx, a, "POST", "/trip/preview", map[string]any{"schemaVersion": 1, "pickup": map[string]float64{"latitude": a.Config.Latitude, "longitude": a.Config.Longitude}, "destination": place["location"], "destinationPlaceId": b.PlaceID})
		if e == nil {
			preview["simulationDestinationPlaceId"] = b.PlaceID
		}
		return preview, e
	case "book":
		return r.requestKey(ctx, a, "POST", "/trip/start", map[string]any{"rideFareID": b.FareID, "confirmed": true}, b.CommandID)
	case "cancel":
		return r.requestKey(ctx, a, "POST", "/rider/trips/"+b.TripID+"/cancel", map[string]any{"clientCommandId": b.CommandID, "confirmed": true, "reason": b.Reason}, b.CommandID)
	case "receipt":
		return r.request(ctx, a, "GET", "/rider/trips/"+b.TripID+"/receipt", nil)
	}
	return nil, errors.New("unsupported action")
}
func (r *Runtime) riderCommands(ctx context.Context, run string, actors map[string]*liveActor, concurrency int) {
	rows, e := r.Pool.Query(ctx, `SELECT id::text,participant_id::text,payload FROM simulation_rider_actions WHERE simulation_run_id=$1::uuid AND status='queued' AND scheduled_at<=NOW() ORDER BY scheduled_at,created_at LIMIT 100`, run)
	if e != nil {
		return
	}
	type pending struct {
		id, actor string
		raw       []byte
	}
	var list []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.actor, &p.raw) == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	if concurrency < 1 || concurrency > 100 {
		return
	}
	slots := make(chan struct{}, concurrency)
	var group sync.WaitGroup
	for _, p := range list {
		p := p
		group.Add(1)
		go func() {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			a := actors[p.actor]
			if a == nil {
				return
			}
			claimed, e := r.Pool.Exec(ctx, `UPDATE simulation_rider_actions SET status='processing' WHERE id=$1::uuid AND status='queued'`, p.id)
			if e != nil || claimed.RowsAffected() != 1 {
				return
			}
			var b RiderAction
			e = json.Unmarshal(p.raw, &b)
			var result map[string]any
			if e == nil && b.OwnerRole() != a.Config.Role {
				e = errors.New("actor role mismatch")
			}
			if e == nil {
				result, e = r.riderAction(ctx, a, b)
			}
			status, reason := "succeeded", ""
			if e != nil {
				status = "failed"
				reason = e.Error()
			}
			raw, _ := json.Marshal(result)
			r.Pool.Exec(ctx, `UPDATE simulation_rider_actions SET status=$2,result=$3::jsonb,error=NULLIF($4,''),processed_at=NOW() WHERE id=$1::uuid`, p.id, status, raw, reason)
			r.event(run, p.actor, "rider."+b.Action+"."+status, map[string]any{"commandId": p.id, "reason": reason})
		}()
	}
	group.Wait()
}
