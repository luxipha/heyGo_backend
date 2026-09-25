package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/proto/driver"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxWebSocketMessageBytes = 64 << 10

// RidersWSHandler handles WebSocket connections for riders
func RidersWSHandler(ctx *gin.Context, kfc *kafka.KafkaClient, connManager *messaging.ConnectionManager, pool *pgxpool.Pool) {
	conn, err := connManager.Upgrade(ctx.Writer, ctx.Request)
	if err != nil {
		logs.L().Errorw("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxWebSocketMessageBytes)

	user, _ := gatewayauth.CurrentUser(ctx)
	riderID := user.ID

	cancelStream, err := attachEventStream(ctx.Request.Context(), connManager, conn, messaging.NewEventStore(pool), riderID, ctx.Query("afterEventId"))
	if err != nil {
		logs.L().Errorw("failed to attach rider event stream", "riderID", riderID, "error", err)
		return
	}
	defer cancelStream()
	defer connManager.RemoveIf(riderID, conn)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			logs.L().Errorw("Error reading message", "error", err)
			break
		}
		logs.L().Infow("Received message to rider", "riderID", riderID)
	}
}

// DriversWSHandler handles WebSocket connections for drivers
func DriversWSHandler(ctx *gin.Context, kfc *kafka.KafkaClient, connManager *messaging.ConnectionManager, pool *pgxpool.Pool) {
	conn, err := connManager.Upgrade(ctx.Writer, ctx.Request)
	if err != nil {
		logs.L().Errorw("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxWebSocketMessageBytes)

	user, _ := gatewayauth.CurrentUser(ctx)
	driverID := user.ID
	sessionID := uuid.NewString()

	identity := &driver.Driver{Id: driverID}
	err = pool.QueryRow(ctx.Request.Context(), `SELECT p.display_name,p.photo_url,COALESCE(v.plate,''),COALESCE(v.package_slug,'')
		FROM driver_profiles p LEFT JOIN driver_vehicles v ON v.driver_id=p.driver_id WHERE p.driver_id=$1::UUID`, driverID).Scan(&identity.Name, &identity.ProfilePic, &identity.CarPlate, &identity.PackageSlug)
	if err != nil && err != pgx.ErrNoRows {
		logs.L().Errorw("driver identity unavailable", "driverID", driverID, "error", err)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "driver identity unavailable"), time.Now().Add(time.Second))
		return
	}

	if _, err := pool.Exec(ctx.Request.Context(), `INSERT INTO driver_socket_sessions(driver_id,session_id,expires_at)
		VALUES($1::UUID,$2::UUID,NOW()+INTERVAL '30 seconds')
		ON CONFLICT(driver_id) DO UPDATE SET session_id=EXCLUDED.session_id,expires_at=EXCLUDED.expires_at`, driverID, sessionID); err != nil {
		logs.L().Errorw("failed to register driver socket session", "driverID", driverID, "error", err)
		return
	}
	defer func() {
		connManager.RemoveIf(driverID, conn)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := pool.Exec(cleanupCtx, `DELETE FROM driver_socket_sessions WHERE driver_id=$1::UUID AND session_id=$2::UUID`, driverID, sessionID)
		if err != nil {
			logs.L().Errorw("failed to clear driver socket session", "driverID", driverID, "error", err)
			return
		}
		if result.RowsAffected() == 0 {
			return // Another gateway now owns this driver's socket.
		}
		if _, err := pool.Exec(cleanupCtx, `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,last_seen_at=NOW(),updated_at=NOW() WHERE id=$1::UUID
			AND NOT EXISTS(SELECT 1 FROM driver_socket_sessions WHERE driver_id=$1::UUID AND expires_at>NOW())`, driverID); err != nil {
			logs.L().Errorw("failed to mark disconnected driver offline", "driverID", driverID, "error", err)
		}
	}()
	store := messaging.NewEventStore(pool)
	cancelStream, err := attachEventStreamForSession(ctx.Request.Context(), connManager, conn, store, driverID, sessionID, ctx.Query("afterEventId"))
	if err != nil {
		logs.L().Errorw("failed to replay driver events", "driverID", driverID, "error", err)
		return
	}
	defer cancelStream()
	requestCtx := ctx.Request.Context()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-requestCtx.Done():
				return
			case <-ticker.C:
				result, err := pool.Exec(requestCtx, `UPDATE driver_socket_sessions SET expires_at=NOW()+INTERVAL '30 seconds' WHERE driver_id=$1::UUID AND session_id=$2::UUID`, driverID, sessionID)
				if err != nil {
					logs.L().Warnw("failed to refresh driver socket session", "driverID", driverID, "error", err)
					continue
				}
				if result.RowsAffected() == 0 {
					_ = conn.Close() // Another gateway has replaced this socket.
					return
				}
			}
		}
	}()

	registration, err := json.Marshal(identity)
	if err != nil {
		return
	}
	if _, err := store.Append(ctx.Request.Context(), driverID, uuid.NewString(), contracts.DriverCmdRegister, registration); err != nil {
		logs.L().Errorw("failed to store register message for driver", "driverID", driverID, "error", err)
		return
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived, websocket.ClosePolicyViolation) {
				logs.L().Errorw("error reading message", "error", err)
			}
			break
		}
		var ownsSession bool
		if err := pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_socket_sessions WHERE driver_id=$1::UUID AND session_id=$2::UUID AND expires_at>NOW())`, driverID, sessionID).Scan(&ownsSession); err != nil || !ownsSession {
			logs.L().Warnw("driver socket session is no longer current", "driverID", driverID, "error", err)
			break
		}

		type driverMessage struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		var dm driverMessage
		if err := json.Unmarshal(message, &dm); err != nil {
			logs.L().Warnw("Failed to unmarshal message", "error", err)
			continue
		}

		switch dm.Type {
		case contracts.DriverCmdLocation:
			var location messaging.DriverLocationData
			if err := json.Unmarshal(dm.Data, &location); err != nil || !validCoordinate(location.Location.Latitude, location.Location.Longitude) || (location.Location.Heading != nil && (math.IsNaN(*location.Location.Heading) || *location.Location.Heading < 0 || *location.Location.Heading >= 360)) || (location.Location.Speed != nil && (math.IsNaN(*location.Location.Speed) || *location.Location.Speed < 0)) {
				logs.L().Warnw("Invalid driver location", "driverID", driverID, "error", err)
				continue
			}
			// Persist the trusted socket reading before acknowledging it through
			// Kafka. Go Online can then use a location sent while still offline.
			locationTx, err := pool.Begin(ctx.Request.Context())
			if err != nil {
				logs.L().Errorw("failed to begin driver location update", "driverID", driverID, "error", err)
				continue
			}
			if _, err := locationTx.Exec(ctx.Request.Context(), `INSERT INTO driver_live_locations(driver_id,location,recorded_at)
				VALUES($1::UUID,ST_SetSRID(ST_MakePoint($3,$2),4326)::geography,NOW())
				ON CONFLICT(driver_id) DO UPDATE SET location=EXCLUDED.location,recorded_at=EXCLUDED.recorded_at`, driverID, location.Location.Latitude, location.Location.Longitude); err != nil {
				_ = locationTx.Rollback(ctx.Request.Context())
				logs.L().Errorw("failed to persist driver location", "driverID", driverID, "error", err)
				continue
			}
			if _, err := locationTx.Exec(ctx.Request.Context(), `SELECT refresh_driver_operating_market($1::UUID)`, driverID); err != nil {
				_ = locationTx.Rollback(ctx.Request.Context())
				logs.L().Errorw("failed to refresh driver market", "driverID", driverID, "error", err)
				continue
			}
			if err := locationTx.Commit(ctx.Request.Context()); err != nil {
				logs.L().Errorw("failed to commit driver location", "driverID", driverID, "error", err)
				continue
			}
			trustedData, _ := json.Marshal(location)
			if err := kfc.Producer.SendMessage(ctx, dm.Type, &contracts.KafkaMessage{EntityID: driverID, Data: trustedData}); err != nil {
				logs.L().Errorw("failed to publish driver location", "driverID", driverID, "error", err)
			}
		case contracts.DriverCmdTripAccept, contracts.DriverCmdTripDecline:
			var response messaging.DriverTripResponseData
			if err := json.Unmarshal(dm.Data, &response); err != nil || response.TripID == "" {
				logs.L().Warnw("Invalid driver trip response", "driverID", driverID, "error", err)
				continue
			}
			if _, err := uuid.Parse(response.TripID); err != nil {
				continue
			}
			response.Driver = identity
			response.RiderID = ""
			trustedData, err := json.Marshal(response)
			if err != nil {
				logs.L().Warnw("Failed to marshal driver trip response", "driverID", driverID, "error", err)
				continue
			}
			// Notify trip service about trip acceptance/decline
			if err := kfc.Producer.SendMessageAndWait(ctx, dm.Type, &contracts.KafkaMessage{
				EntityID: driverID,
				Data:     trustedData,
			}, 5*time.Second); err != nil {
				logs.L().Errorw("failed to send message to trip service", "error", err)
				continue
			}
		default:
			logs.L().Warnw("Unknown message from driver", "driverID", driverID, "type", dm.Type)
		}
	}
}
