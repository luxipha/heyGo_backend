package handler

import (
	"github.com/gin-gonic/gin"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strconv"
	"time"
)

func (a *riderTripAPI) profileTrustHistory(c *gin.Context) {
	if a.pool == nil {
		driverError(c, 503, "trust_history_unavailable", "Trust history is unavailable")
		return
	}
	limit, err := parseDriverTripHistoryLimit(c.Query("limit"))
	if err != nil {
		driverError(c, 400, "invalid_limit", err.Error())
		return
	}
	var after any
	if raw := c.Query("cursor"); raw != "" {
		value, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || value <= 0 {
			driverError(c, 400, "invalid_cursor", "Trust cursor is invalid")
			return
		}
		after = value
	}
	user, _ := gatewayauth.CurrentUser(c)
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id,score,status,calculated_at,policy_version,recorded_at FROM consumer_trust_snapshots WHERE user_id=$1::UUID AND ($2::BIGINT IS NULL OR id<$2) ORDER BY id DESC LIMIT $3`, user.ID, after, limit+1)
	if err != nil {
		driverError(c, 503, "trust_history_unavailable", "Trust history is unavailable")
		return
	}
	defer rows.Close()
	items := []gin.H{}
	ids := []int64{}
	for rows.Next() {
		var id int64
		var score int
		var status, policy string
		var calculated *time.Time
		var recorded time.Time
		if err := rows.Scan(&id, &score, &status, &calculated, &policy, &recorded); err != nil {
			driverError(c, 503, "trust_history_unavailable", "Trust history is unavailable")
			return
		}
		ids = append(ids, id)
		items = append(items, gin.H{"score": score, "status": status, "calculatedAt": calculated, "policyVersion": policy, "recordedAt": recorded})
	}
	if rows.Err() != nil {
		driverError(c, 503, "trust_history_unavailable", "Trust history is unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		v := strconv.FormatInt(ids[limit-1], 10)
		next = &v
		items = items[:limit]
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items, "nextCursor": next, "source": "consumer_snapshot", "completeCalculationHistory": false}})
}

// Metrics count persisted completed trips, independently of history pagination.
func (a *riderTripAPI) profileMetrics(c *gin.Context) {
	if a.pool == nil {
		driverError(c, 503, "profile_unavailable", "Profile is unavailable")
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	var completed, received int64
	err := a.pool.QueryRow(c.Request.Context(), `SELECT
 (SELECT COUNT(*) FROM trips WHERE rider_id=$1::UUID AND status='completed'),
 (SELECT COUNT(*) FROM trip_ratings r JOIN trips t ON t.id=r.trip_id WHERE t.rider_id=$1::UUID AND r.subject_id=$1::UUID AND r.actor_id=t.assigned_driver_id AND r.actor_id<>r.subject_id AND t.status='completed')`, user.ID).Scan(&completed, &received)
	if err != nil {
		driverError(c, 503, "profile_unavailable", "Profile is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"completedTrips": completed, "receivedFeedbackCount": received}})
}

type riderReceivedFeedback struct {
	TripID    string    `json:"tripId"`
	Rating    int       `json:"rating"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"createdAt"`
}

// Only participant Driver ratings are disclosed. Private identities, moderation
// records and raw comments are excluded until a Rider disclosure policy exists.
func (a *riderTripAPI) profileFeedback(c *gin.Context) {
	if a.pool == nil {
		driverError(c, 503, "feedback_unavailable", "Feedback is unavailable")
		return
	}
	limit, err := parseDriverTripHistoryLimit(c.Query("limit"))
	if err != nil {
		driverError(c, 400, "invalid_limit", err.Error())
		return
	}
	var at, id any
	if raw := c.Query("cursor"); raw != "" {
		cursor, e := decodeDriverTripHistoryCursor(raw)
		if e != nil {
			driverError(c, 400, "invalid_cursor", "Feedback cursor is invalid")
			return
		}
		at, id = cursor.OccurredAt, cursor.ID
	}
	user, _ := gatewayauth.CurrentUser(c)
	rows, err := a.pool.Query(c.Request.Context(), `SELECT r.trip_id::TEXT,r.rating,r.feedback_tags,r.created_at FROM trip_ratings r JOIN trips t ON t.id=r.trip_id WHERE t.rider_id=$1::UUID AND r.subject_id=$1::UUID AND r.actor_id=t.assigned_driver_id AND r.actor_id<>r.subject_id AND t.status='completed' AND ($2::TIMESTAMPTZ IS NULL OR (r.created_at,r.trip_id)<($2,$3::UUID)) ORDER BY r.created_at DESC,r.trip_id DESC LIMIT $4`, user.ID, at, id, limit+1)
	if err != nil {
		driverError(c, 503, "feedback_unavailable", "Feedback is unavailable")
		return
	}
	defer rows.Close()
	items := []riderReceivedFeedback{}
	for rows.Next() {
		var item riderReceivedFeedback
		if err := rows.Scan(&item.TripID, &item.Rating, &item.Tags, &item.CreatedAt); err != nil {
			driverError(c, 503, "feedback_unavailable", "Feedback is unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(c, 503, "feedback_unavailable", "Feedback is unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		v := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: last.CreatedAt, ID: last.TripID})
		next = &v
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items, "nextCursor": next}})
}
