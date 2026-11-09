package handler

import (
	"encoding/base64"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strings"
	"time"
)

const riderConversationPageSize = 20

// One statement gives the inbox a consistent owner-scoped view. Message pairing
// and terminal retention mirror tripChatAPI.access/list, including reassignment.
const riderMessageSummarySQL = `WITH eligible AS MATERIALIZED (
 SELECT t.id,t.status,t.assigned_driver_id,
 CASE WHEN t.status='completed' THEN COALESCE(t.completed_at,t.updated_at)
 WHEN t.status='cancelled' THEN COALESCE(t.cancelled_at,t.updated_at) END ended_at,
 COALESCE(NULLIF(d.name,''),'Your driver') driver_name,f.package_slug
 FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id LEFT JOIN drivers d ON d.id=t.assigned_driver_id
 WHERE t.rider_id=$1::uuid AND t.assigned_driver_id IS NOT NULL
 AND (t.status IN ('accepted','arrived','started') OR
 (t.status IN ('completed','cancelled') AND CASE WHEN t.status='completed' THEN COALESCE(t.completed_at,t.updated_at) ELSE COALESCE(t.cancelled_at,t.updated_at) END >= NOW()-INTERVAL '30 days'))
), conversations AS MATERIALIZED (
 SELECT e.*,m.body,m.created_at last_at,
 (SELECT count(*) FROM trip_chat_messages u WHERE u.trip_id=e.id AND u.sender_id=e.assigned_driver_id AND u.recipient_id=$1::uuid AND u.read_at IS NULL) unread_count,
 jsonb_build_object('tripId',e.id,'driverName',e.driver_name,'packageSlug',e.package_slug,
 'current',e.ended_at IS NULL,'endedAt',e.ended_at,'lastMessage',m.body,'lastAt',m.created_at) detail
 FROM eligible e LEFT JOIN LATERAL (
 SELECT body,created_at FROM trip_chat_messages m WHERE m.trip_id=e.id
 AND ((m.sender_id=$1::uuid AND m.recipient_id=e.assigned_driver_id) OR (m.recipient_id=$1::uuid AND m.sender_id=e.assigned_driver_id))
 ORDER BY created_at DESC,id DESC LIMIT 1
 ) m ON true
), channels AS (
 SELECT kind.type,
 (SELECT count(*) FROM rider_notifications n WHERE n.rider_id=$1::uuid AND n.type=kind.type AND n.read_at IS NULL) unread_count,
 (SELECT count(*) FROM rider_notifications n WHERE n.rider_id=$1::uuid AND n.type=kind.type) total_count,
 (SELECT jsonb_build_object('id',n.id::text,'type',n.type,'title',n.title,'message',n.message,'data',n.data,'createdAt',n.created_at,'readAt',n.read_at)
 FROM rider_notifications n WHERE n.rider_id=$1::uuid AND n.type=kind.type ORDER BY id DESC LIMIT 1) latest
 FROM (VALUES ('promotions'),('feedback'),('account_alerts')) kind(type)
), past_page AS (
 SELECT * FROM conversations WHERE ended_at IS NOT NULL AND last_at IS NOT NULL
 AND ($2::timestamptz IS NULL OR (ended_at,id)<($2,$3::uuid))
 ORDER BY ended_at DESC,id DESC LIMIT $4
)
SELECT jsonb_build_object(
 'channels',(SELECT jsonb_agg(jsonb_build_object('type',type,'unreadCount',unread_count,'totalCount',total_count,'latest',latest) ORDER BY type) FROM channels),
 'currentTrip',(SELECT detail || jsonb_build_object('unreadCount',unread_count) FROM conversations WHERE ended_at IS NULL ORDER BY id LIMIT 1),
 'pastTrips',COALESCE((SELECT jsonb_agg(detail || jsonb_build_object('unreadCount',unread_count) ORDER BY ended_at DESC,id DESC) FROM past_page),'[]'::jsonb),
 'totalUnread',COALESCE((SELECT sum(unread_count) FROM channels),0)+COALESCE((SELECT sum(unread_count) FROM conversations),0))`

type riderConversationSummary struct {
	TripID      string     `json:"tripId"`
	DriverName  string     `json:"driverName"`
	PackageSlug string     `json:"packageSlug"`
	Current     bool       `json:"current"`
	EndedAt     *time.Time `json:"endedAt"`
	LastMessage *string    `json:"lastMessage"`
	LastAt      *time.Time `json:"lastAt"`
	UnreadCount int64      `json:"unreadCount"`
}
type riderChannelSummary struct {
	Type        string             `json:"type"`
	UnreadCount int64              `json:"unreadCount"`
	TotalCount  int64              `json:"totalCount"`
	Latest      *riderNotification `json:"latest"`
}
type riderMessagesSummary struct {
	Channels    []riderChannelSummary      `json:"channels"`
	CurrentTrip *riderConversationSummary  `json:"currentTrip"`
	PastTrips   []riderConversationSummary `json:"pastTrips"`
	TotalUnread int64                      `json:"totalUnread"`
}

func (a *riderAccountAPI) riderMessagesSummary(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	var at *time.Time
	var id *string
	if raw := c.Query("cursor"); raw != "" {
		decoded, e := base64.RawURLEncoding.DecodeString(raw)
		parts := strings.SplitN(string(decoded), "|", 2)
		if e != nil || len(parts) != 2 {
			driverError(c, 400, "invalid_cursor", "Conversation cursor is invalid")
			return
		}
		stamp, e := time.Parse(time.RFC3339Nano, parts[0])
		key, e2 := uuid.Parse(parts[1])
		if e != nil || e2 != nil {
			driverError(c, 400, "invalid_cursor", "Conversation cursor is invalid")
			return
		}
		text := key.String()
		at = &stamp
		id = &text
	}
	var raw []byte
	e := a.pool.QueryRow(c.Request.Context(), riderMessageSummarySQL, riderAccountOwner(c), at, id, riderConversationPageSize+1).Scan(&raw)
	var out riderMessagesSummary
	if e != nil || json.Unmarshal(raw, &out) != nil {
		driverError(c, 503, "messages_unavailable", "Messages are unavailable. Try again.")
		return
	}
	var next *string
	if len(out.PastTrips) > riderConversationPageSize {
		out.PastTrips = out.PastTrips[:riderConversationPageSize]
		last := out.PastTrips[len(out.PastTrips)-1]
		key := base64.RawURLEncoding.EncodeToString([]byte(last.EndedAt.UTC().Format(time.RFC3339Nano) + "|" + last.TripID))
		next = &key
	}
	c.JSON(200, contracts.APIResponse{Data: out, Meta: contracts.APIMeta{NextCursor: next}})
}
