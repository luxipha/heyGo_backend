package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"net/http"
	"strings"
	"time"
)

type riderCommandAPI struct {
	pool *pgxpool.Pool
	bus  *pubsub.Client
}

func registerRiderCommandRoutes(g *gin.RouterGroup, pool *pgxpool.Pool, bus *pubsub.Client) {
	a := &riderCommandAPI{pool: pool, bus: bus}
	r := g.Group("/rider", gatewayauth.RequireRole("rider"))
	r.POST("/trips/:tripID/cancel", func(c *gin.Context) { a.queue(c, contracts.TripCmdCancel) })
	r.POST("/trips/:tripID/rating", func(c *gin.Context) { a.queue(c, contracts.TripCmdRate) })
	r.GET("/trips/:tripID/commands/:commandID", a.read)
}
func (a *riderCommandAPI) queue(c *gin.Context, command string) {
	user, _ := gatewayauth.CurrentUser(c)
	trip := c.Param("tripID")
	if _, err := uuid.Parse(trip); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var b struct {
		ClientCommandID string `json:"clientCommandId"`
		Confirmed       bool   `json:"confirmed"`
		Reason          string `json:"reason"`
		Rating          int    `json:"rating"`
		Comment         string `json:"comment"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_command", "A valid trip action is required")
		return
	}
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" {
		driverError(c, 400, "idempotency_key_required", "A stable request identity is required")
		return
	}
	id, err := uuid.Parse(b.ClientCommandID)
	if err != nil {
		driverError(c, 400, "invalid_command_id", "A stable clientCommandId is required")
		return
	}
	p := messaging.TripLifecycleCommand{TripID: trip, ActorID: user.ID, CommandID: id.String()}
	if command == contracts.TripCmdCancel {
		p.Reason = strings.TrimSpace(b.Reason)
		if !b.Confirmed || p.Reason == "" || len([]rune(p.Reason)) > 500 {
			driverError(c, 400, "confirmation_required", "Confirm cancellation and choose a reason")
			return
		}
	} else {
		p.Rating = b.Rating
		p.Comment, err = normalizeRiderRatingComment(b.Comment)
		if !b.Confirmed || b.Rating < 1 || b.Rating > 5 || err != nil {
			driverError(c, 400, "invalid_rating", "Confirm a rating from 1 to 5 with an optional short comment")
			return
		}
	}
	if a.pool == nil {
		driverError(c, 503, "command_unavailable", "Trip actions are unavailable")
		return
	}
	raw, _ := json.Marshal(p)
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "command_unavailable", "Trip actions are unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	var owned string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM trips WHERE id=$1::UUID AND rider_id=$2::UUID FOR UPDATE`, trip, user.ID).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "command_unavailable", "Trip actions are unavailable")
		return
	}
	result, err := tx.Exec(c.Request.Context(), `INSERT INTO rider_trip_commands(id,rider_id,trip_id,command,payload) VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5::JSONB) ON CONFLICT(id) DO NOTHING`, id, user.ID, trip, command, raw)
	if err != nil {
		driverError(c, 503, "command_unavailable", "Trip actions are unavailable")
		return
	}
	if result.RowsAffected() == 0 {
		var same bool
		err = tx.QueryRow(c.Request.Context(), `SELECT rider_id=$2::UUID AND trip_id=$3::UUID AND command=$4 AND payload=$5::JSONB FROM rider_trip_commands WHERE id=$1::UUID`, id, user.ID, trip, command, raw).Scan(&same)
		if err != nil || !same {
			driverError(c, 409, "command_id_reused", "This request identity belongs to another action")
			return
		}
	} else {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt) SELECT $1::UUID,$2,$3::UUID,$4::JSONB,COALESCE(MAX(attempt),0)+1 FROM trip_event_outbox WHERE trip_id=$1::UUID AND topic=$2 AND recipient_id=$3::UUID`, trip, command, user.ID, raw)
		if err != nil {
			driverError(c, 503, "command_unavailable", "Trip actions are unavailable")
			return
		}
	}
	if tx.Commit(c.Request.Context()) != nil {
		driverError(c, 503, "command_unavailable", "Trip action could not be recorded")
		return
	}
	a.drain(c)
	c.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"commandId": id.String(), "tripId": trip, "status": "queued", "outcome": "unresolved"}})
}
func (a *riderCommandAPI) read(c *gin.Context) {
	user, _ := gatewayauth.CurrentUser(c)
	trip, id := c.Param("tripID"), c.Param("commandID")
	if _, err := uuid.Parse(trip); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_command_id", "Valid command ID is required")
		return
	}
	if a.pool == nil {
		driverError(c, 503, "command_unavailable", "Trip action is unavailable")
		return
	}
	var command, status string
	var reason *string
	var created time.Time
	var resolved *time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT command,status,reason,created_at,resolved_at FROM rider_trip_commands WHERE id=$1::UUID AND trip_id=$2::UUID AND rider_id=$3::UUID`, id, trip, user.ID).Scan(&command, &status, &reason, &created, &resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "command_not_found", "Trip action was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "command_unavailable", "Trip action is unavailable")
		return
	}
	if status == "queued" && a.bus != nil {
		a.drain(c)
		if err := a.pool.QueryRow(c.Request.Context(), `SELECT status,reason,resolved_at FROM rider_trip_commands WHERE id=$1::UUID AND rider_id=$2::UUID`, id, user.ID).Scan(&status, &reason, &resolved); err != nil {
			driverError(c, 503, "command_unavailable", "Trip action is unavailable")
			return
		}
	}
	outcome := status
	if status == "queued" {
		outcome = "unresolved"
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"commandId": id, "tripId": trip, "command": command, "status": status, "outcome": outcome, "reason": reason, "createdAt": created, "resolvedAt": resolved}})
}

func (a *riderCommandAPI) drain(c *gin.Context) {
	if a.bus != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		_, _ = messaging.DrainOutbox(ctx, a.pool, a.bus.Producer, 10)
	}
}
