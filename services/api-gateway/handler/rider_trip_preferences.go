package handler

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"strings"
)

var preferenceLabels = map[string]map[string]string{
	"conversation": {"no_preference": "No conversation preference", "quiet": "Quiet ride", "happy_to_chat": "Happy to chat"},
	"air":          {"either": "No AC preference", "ac_on": "AC on", "windows_down": "Windows down"},
	"music":        {"driver_choice": "Driver’s choice of music", "low": "Low music volume", "off": "Music off"},
}

func validTripPreferences(p map[string]string) bool {
	if p == nil {
		return false
	}
	if len(p) == 0 {
		return true
	}
	if len(p) != 3 {
		return false
	}
	for k, labels := range preferenceLabels {
		if _, ok := labels[p[k]]; !ok {
			return false
		}
	}
	return true
}
func setRiderPreferences(pool *pgxpool.Pool, trip bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var body struct {
			Preferences map[string]string `json:"preferences"`
			Revision    *int64            `json:"expectedRevision"`
		}
		id, err := uuid.Parse(c.Param("fareID"))
		if trip {
			id, err = uuid.Parse(c.Param("tripID"))
		}
		if err != nil || c.ShouldBindJSON(&body) != nil || !validTripPreferences(body.Preferences) || (trip && (body.Revision == nil || *body.Revision < 0)) {
			driverError(c, 400, "invalid_preferences", "Choose valid conversation, air conditioning and music preferences")
			return
		}
		if pool == nil {
			accountError(c)
			return
		}
		ctx := c.Request.Context()
		raw, _ := json.Marshal(body.Preferences)
		if !trip {
			r, e := pool.Exec(ctx, `UPDATE ride_fares SET ride_preferences=$3::jsonb WHERE id=$1::uuid AND rider_id=$2::uuid AND consumed_at IS NULL AND expires_at>NOW()`, id, riderAccountOwner(c), raw)
			if e != nil {
				accountError(c)
				return
			}
			if r.RowsAffected() != 1 {
				driverError(c, 409, "fare_unavailable", "Refresh the quote before changing ride preferences")
				return
			}
			c.JSON(200, contracts.APIResponse{Data: gin.H{"preferences": body.Preferences}})
			return
		}
		tx, e := pool.Begin(ctx)
		if e != nil {
			accountError(c)
			return
		}
		defer tx.Rollback(ctx)
		var old json.RawMessage
		var revision int64
		var status, fare string
		var driver *string
		e = tx.QueryRow(ctx, `SELECT ride_preferences,preferences_revision,status,ride_fare_id::text,assigned_driver_id::text FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, id, riderAccountOwner(c)).Scan(&old, &revision, &status, &fare, &driver)
		if e != nil {
			driverError(c, 404, "trip_not_found", "Trip was not found")
			return
		}
		if status != "accepted" && status != "arrived" && status != "started" {
			driverError(c, 409, "preferences_unavailable", "Ride preferences can only change on an accepted active trip")
			return
		}
		var existing map[string]string
		_ = json.Unmarshal(old, &existing)
		same := len(existing) == len(body.Preferences)
		for k, v := range existing {
			if body.Preferences[k] != v {
				same = false
			}
		}
		if !same {
			if revision != *body.Revision {
				driverError(c, 409, "preferences_changed", "Preferences changed. Refresh the trip before saving again")
				return
			}
			revision++
			if _, e = tx.Exec(ctx, `UPDATE trips SET ride_preferences=$2::jsonb,preferences_revision=$3,updated_at=NOW() WHERE id=$1::uuid`, id, raw, revision); e != nil {
				accountError(c)
				return
			}
			if _, e = tx.Exec(ctx, `UPDATE ride_fares SET ride_preferences=$2::jsonb WHERE id=$1::uuid`, fare, raw); e != nil {
				accountError(c)
				return
			}
			source := fmt.Sprintf("trip:%s:preferences:%d", id, revision)
			payload := gin.H{"tripId": id.String(), "preferences": body.Preferences, "preferencesRevision": revision, "refreshTrip": true}
			if e = tripOptionsEvent(ctx, tx, id.String(), source, "trip.event.preferences_updated", payload); e != nil {
				accountError(c)
				return
			}
			if driver != nil {
				parts := []string{}
				for _, k := range []string{"conversation", "air", "music"} {
					if label := preferenceLabels[k][body.Preferences[k]]; label != "" {
						parts = append(parts, label)
					}
				}
				message := "Passenger updated ride preferences: " + strings.Join(parts, " · ")
				if len(parts) == 0 {
					message = "Passenger cleared their ride preferences"
				}
				data, _ := json.Marshal(payload)
				if e = messaging.AppendNotificationTx(ctx, tx, *driver, source+":notification", messaging.InboxNotification{Type: "dispatch", Title: "Ride preferences updated", Message: message, Data: data}); e != nil {
					accountError(c)
					return
				}
			}
		}
		if e = tx.Commit(ctx); e != nil {
			accountError(c)
			return
		}
		c.JSON(200, contracts.APIResponse{Data: gin.H{"preferences": body.Preferences, "preferencesRevision": revision}})
	}
}
