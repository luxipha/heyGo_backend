package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

var rideAgentClient = &http.Client{Timeout: 12 * time.Second}

func callRideAgent(ctx context.Context, body agent.Request) (agent.Response, error) {
	var reply agent.Response
	base := strings.TrimRight(os.Getenv("RIDE_AGENT_URL"), "/")
	token := os.Getenv("RIDE_AGENT_INTERNAL_TOKEN")
	if base == "" || len(token) < 32 {
		return reply, errors.New("ride agent unavailable")
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/resolve", bytes.NewReader(raw))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if err := sharedauth.AddCloudRunIdentity(req, os.Getenv("RIDE_AGENT_AUDIENCE")); err != nil {
		return reply, errors.New("ride agent identity unavailable")
	}
	res, err := rideAgentClient.Do(req)
	if err != nil {
		return reply, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return reply, errors.New("ride agent unavailable")
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 256<<10))
	if err := decoder.Decode(&reply); err != nil {
		return reply, err
	}
	validState := reply.State == "resolved" || reply.State == "clarification" || reply.State == "unavailable" || reply.State == "unsupported" || reply.State == "action" || reply.State == "suggestions"
	if reply.SchemaVersion != 1 || !reply.ConfirmationRequired || !validState || len(reply.Candidates) > 5 || len(reply.Message) > 1024 {

		return reply, errors.New("unsupported ride agent version")
	}
	if reply.State == "action" {
		if body.Context == nil || len(reply.Candidates) != 0 || reply.Place != nil {
			return reply, errors.New("invalid agent action proposal")
		}
		switch reply.Action {
		case "resolve_saved_place":
			if reply.SavedPlaceLabel != "Home" && reply.SavedPlaceLabel != "Work" {
				return reply, errors.New("invalid saved place label")
			}
		case "preview_ride", "request_ride":
			if !body.Context.HasDestination || reply.SavedPlaceLabel != "" || (reply.Action == "request_ride" && !body.Context.HasQuote) {
				return reply, errors.New("invalid agent action proposal")
			}
		default:
			return reply, errors.New("invalid agent action proposal")
		}
	} else if reply.Action != "" || reply.SavedPlaceLabel != "" {
		return reply, errors.New("unexpected agent action")
	}

	if reply.State == "suggestions" {
		if !body.Autocomplete || len(reply.Candidates) != 0 || reply.Place != nil || len(reply.Suggestions) > 5 {
			return reply, errors.New("invalid suggestions")
		}
		for _, s := range reply.Suggestions {
			if s.ID == "" || s.Name == "" || len(s.ID) > 256 || len(s.Name) > 256 || len(s.Address) > 1024 {
				return reply, errors.New("invalid suggestion")
			}
		}
	} else if body.Autocomplete || len(reply.Suggestions) > 0 {
		return reply, errors.New("unexpected suggestions")
	}

	for _, p := range reply.Candidates {
		if p.ID == "" || p.Name == "" || !agent.ValidLocation(p.Location) {
			return reply, errors.New("invalid place result")
		}
	}
	if reply.State == "resolved" && (reply.Place == nil || len(reply.Candidates) != 1 || reply.Place.ID != reply.Candidates[0].ID || (body.PlaceID != "" && reply.Place.ID != body.PlaceID)) {
		return reply, errors.New("invalid resolved identity")
	}
	return reply, nil
}
func registerRideAgentRoutes(authenticated *gin.RouterGroup, pools ...*pgxpool.Pool) {
	rider := authenticated.Group("/rider", gatewayauth.RequireRole("rider"))
	rider.POST("/agent/resolve", func(ctx *gin.Context) {
		if len(pools) > 0 {
			status, err := geo.ReadSettings(ctx.Request.Context(), pools[0])
			if err != nil || !status.Settings.PlacesEnabled() {
				driverError(ctx, 503, "places_unavailable", "Destination lookup is unavailable")
				return
			}
		}

		ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4096)
		var body agent.Request
		if err := ctx.ShouldBindJSON(&body); err != nil || body.SchemaVersion != 1 || len(body.Message) > 500 || len(body.PlaceID) > 256 {
			driverError(ctx, 400, "invalid_agent_request", "Versioned ride destination request is required")
			return
		}
		reply, err := callRideAgent(ctx.Request.Context(), body)
		if err != nil {
			driverError(ctx, 503, "places_unavailable", "Destination lookup is unavailable; try again")
			return
		}
		ctx.JSON(200, contracts.APIResponse{Data: reply})
	})
}
