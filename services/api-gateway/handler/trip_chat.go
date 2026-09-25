package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	tripChatMaxBodyRunes = 2000
	tripChatDefaultLimit = 50
	tripChatMaxLimit     = 100
	tripChatReadWindow   = 30 * 24 * time.Hour
)

type tripChatAccess struct {
	RiderID       string
	DriverID      string
	Status        string
	EndedAt       *time.Time
	ParticipantID string
	OtherID       string
}

type tripChatMessage struct {
	ID              string     `json:"id"`
	TripID          string     `json:"tripId"`
	SenderID        string     `json:"senderId"`
	ClientMessageID string     `json:"clientMessageId"`
	Body            string     `json:"body"`
	CreatedAt       time.Time  `json:"createdAt"`
	ReadAt          *time.Time `json:"readAt,omitempty"`
}

func registerTripChatRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool) {
	chat := &tripChatAPI{pool: pool}
	authenticated.GET("/trips/:tripID/messages", chat.list)
	authenticated.POST("/trips/:tripID/messages", chat.send)
	authenticated.POST("/trips/:tripID/messages/read", chat.markRead)
}

type tripChatAPI struct{ pool *pgxpool.Pool }

func (a *tripChatAPI) access(ctx *gin.Context, tripID string) (*tripChatAccess, bool) {
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return nil, false
	}
	user, ok := gatewayauth.CurrentUser(ctx)
	if !ok {
		driverError(ctx, http.StatusUnauthorized, "authentication_required", "Authentication is required")
		return nil, false
	}
	access := &tripChatAccess{}
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT rider_id::TEXT,COALESCE(assigned_driver_id::TEXT,''),status,
		CASE WHEN status='completed' THEN COALESCE(completed_at,updated_at) WHEN status='cancelled' THEN COALESCE(cancelled_at,updated_at) ELSE NULL END
		FROM trips WHERE id=$1::UUID`, tripID).Scan(&access.RiderID, &access.DriverID, &access.Status, &access.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return nil, false
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Trip chat is unavailable")
		return nil, false
	}
	switch user.ID {
	case access.RiderID:
		if !user.HasRole("rider") {
			driverError(ctx, http.StatusForbidden, "chat_forbidden", "Only this trip's rider can access its chat")
			return nil, false
		}
		access.ParticipantID, access.OtherID = access.RiderID, access.DriverID
	case access.DriverID:
		if !user.HasRole("driver") {
			driverError(ctx, http.StatusForbidden, "chat_forbidden", "Only this trip's driver can access its chat")
			return nil, false
		}
		access.ParticipantID, access.OtherID = access.DriverID, access.RiderID
	default:
		driverError(ctx, http.StatusForbidden, "chat_forbidden", "Only this trip's rider and accepted driver can access its chat")
		return nil, false
	}
	if access.DriverID == "" || access.Status == "pending" || access.Status == "assigned" {
		driverError(ctx, http.StatusConflict, "chat_not_open", "Trip chat opens after the driver accepts the trip")
		return nil, false
	}
	if access.Status != "accepted" && access.Status != "arrived" && access.Status != "started" && access.Status != "completed" && access.Status != "cancelled" {
		driverError(ctx, http.StatusConflict, "chat_not_open", "Trip chat is unavailable for this trip status")
		return nil, false
	}
	if access.EndedAt != nil && time.Since(*access.EndedAt) > tripChatReadWindow {
		driverError(ctx, http.StatusGone, "chat_history_expired", "Trip chat history is available for 30 days after the trip ends")
		return nil, false
	}
	return access, true
}

func (a *tripChatAPI) list(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, ok := a.access(ctx, tripID); !ok {
		return
	}
	limit := tripChatDefaultLimit
	if raw := ctx.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > tripChatMaxLimit {
			driverError(ctx, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 100")
			return
		}
		limit = value
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor := ctx.Query("cursor"); cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.SplitN(string(raw), "|", 2)
		if err != nil || len(parts) != 2 {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Chat cursor is invalid")
			return
		}
		stamp, timeErr := time.Parse(time.RFC3339Nano, parts[0])
		messageID, idErr := uuid.Parse(parts[1])
		if timeErr != nil || idErr != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Chat cursor is invalid")
			return
		}
		messageIDText := messageID.String()
		beforeTime, beforeID = &stamp, &messageIDText
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,trip_id::TEXT,sender_id::TEXT,client_message_id::TEXT,body,created_at,read_at
		FROM trip_chat_messages WHERE trip_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2,$3::UUID))
		ORDER BY created_at DESC,id DESC LIMIT $4`, tripID, beforeTime, beforeID, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Trip chat history is unavailable")
		return
	}
	defer rows.Close()
	items := make([]tripChatMessage, 0, limit)
	for rows.Next() {
		var item tripChatMessage
		if err := rows.Scan(&item.ID, &item.TripID, &item.SenderID, &item.ClientMessageID, &item.Body, &item.CreatedAt, &item.ReadAt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Trip chat history is unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Trip chat history is unavailable")
		return
	}
	var nextCursor *string
	if len(items) > limit {
		last := items[limit-1]
		cursor := base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
		nextCursor = &cursor
		items = items[:limit]
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items}, Meta: contracts.APIMeta{NextCursor: nextCursor}})
}

func (a *tripChatAPI) send(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	access, ok := a.access(ctx, tripID)
	if !ok {
		return
	}
	if access.Status != "accepted" && access.Status != "arrived" && access.Status != "started" {
		driverError(ctx, http.StatusConflict, "chat_read_only", "Messages can only be sent during an active trip")
		return
	}
	var body struct {
		ClientMessageID string `json:"clientMessageId" binding:"required"`
		Body            string `json:"body" binding:"required"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message", "Client message ID and message text are required")
		return
	}
	clientID, err := uuid.Parse(body.ClientMessageID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message_id", "Client message ID must be a UUID")
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" || len([]rune(text)) > tripChatMaxBodyRunes {
		driverError(ctx, http.StatusBadRequest, "invalid_message_body", "Message text must contain 1 to 2000 characters")
		return
	}

	messageID := uuid.NewString()
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Message could not be sent")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var lockedStatus string
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT status FROM trips WHERE id=$1::UUID AND rider_id=$2::UUID AND assigned_driver_id=$3::UUID FOR UPDATE`, tripID, access.RiderID, access.DriverID).Scan(&lockedStatus); err != nil {
		driverError(ctx, http.StatusConflict, "chat_read_only", "Trip is no longer active for chat")
		return
	}
	if lockedStatus != "accepted" && lockedStatus != "arrived" && lockedStatus != "started" {
		driverError(ctx, http.StatusConflict, "chat_read_only", "Messages can only be sent during an active trip")
		return
	}
	var item tripChatMessage
	err = tx.QueryRow(ctx.Request.Context(), `INSERT INTO trip_chat_messages(id,trip_id,sender_id,recipient_id,client_message_id,body)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5::UUID,$6)
		ON CONFLICT(trip_id,sender_id,client_message_id) DO NOTHING
		RETURNING id::TEXT,trip_id::TEXT,sender_id::TEXT,client_message_id::TEXT,body,created_at,read_at`, messageID, tripID, access.ParticipantID, access.OtherID, clientID.String(), text).
		Scan(&item.ID, &item.TripID, &item.SenderID, &item.ClientMessageID, &item.Body, &item.CreatedAt, &item.ReadAt)
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,trip_id::TEXT,sender_id::TEXT,client_message_id::TEXT,body,created_at,read_at
			FROM trip_chat_messages WHERE trip_id=$1::UUID AND sender_id=$2::UUID AND client_message_id=$3::UUID`, tripID, access.ParticipantID, clientID.String()).
			Scan(&item.ID, &item.TripID, &item.SenderID, &item.ClientMessageID, &item.Body, &item.CreatedAt, &item.ReadAt)
		if err == nil && item.Body != text {
			driverError(ctx, http.StatusConflict, "message_id_reused", "Client message ID was already used for different message text")
			return
		}
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Message could not be sent")
		return
	}
	if created {
		payload, _ := json.Marshal(map[string]any{"tripId": tripID, "message": item})
		if _, err = messaging.AppendEventTx(ctx.Request.Context(), tx, access.OtherID, "trip-chat-message:"+item.ID, "trip.message.created", payload); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Message could not be sent")
			return
		}
	}
	if err := tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Message could not be sent")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"message": item}})
}

func (a *tripChatAPI) markRead(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	access, ok := a.access(ctx, tripID)
	if !ok {
		return
	}
	var body struct {
		ThroughMessageID string `json:"throughMessageId" binding:"required"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_read_receipt", "throughMessageId is required")
		return
	}
	throughID, err := uuid.Parse(body.ThroughMessageID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_read_receipt", "throughMessageId must be a UUID")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Read state could not be updated")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var throughTime time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT created_at FROM trip_chat_messages WHERE id=$1::UUID AND trip_id=$2::UUID AND recipient_id=$3::UUID`, throughID.String(), tripID, access.ParticipantID).Scan(&throughTime)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "message_not_found", "Unread message was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Read state could not be updated")
		return
	}
	var count int64
	err = tx.QueryRow(ctx.Request.Context(), `WITH updated AS (
		UPDATE trip_chat_messages SET read_at=NOW() WHERE trip_id=$1::UUID AND recipient_id=$2::UUID
		AND read_at IS NULL AND (created_at,id)<=($3,$4::UUID) RETURNING 1
	) SELECT COUNT(*) FROM updated`, tripID, access.ParticipantID, throughTime, throughID.String()).Scan(&count)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Read state could not be updated")
		return
	}
	readAt := time.Now().UTC()
	if count > 0 {
		payload, _ := json.Marshal(map[string]any{"tripId": tripID, "readerId": user.ID, "throughMessageId": throughID.String(), "readAt": readAt})
		if _, err := messaging.AppendEventTx(ctx.Request.Context(), tx, access.OtherID, "trip-chat-read:"+tripID+":"+user.ID+":"+throughID.String(), "trip.message.read", payload); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Read state could not be updated")
			return
		}
	}
	if err := tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "chat_unavailable", "Read state could not be updated")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "throughMessageId": throughID.String(), "readAt": readAt, "updatedMessages": count}})
}
