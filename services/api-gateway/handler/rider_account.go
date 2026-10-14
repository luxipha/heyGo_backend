package handler

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"os"
	"strings"
	"unicode"
)

type riderAccountAPI struct {
	pool        *pgxpool.Pool
	files       storage.ObjectStore
	deliveryKey []byte
}

func registerRiderAccountRoutes(g *gin.RouterGroup, pool *pgxpool.Pool, files storage.ObjectStore) {
	key, _ := hex.DecodeString(os.Getenv("HEYGO_ADMIN_SETTINGS_KEY"))
	a := &riderAccountAPI{pool: pool, files: files, deliveryKey: key}
	r := g.Group("/rider", gatewayauth.RequireRole("rider"))
	r.Use(func(c *gin.Context) {
		if pool == nil {
			driverError(c, 503, "account_unavailable", "Account data is unavailable")
			c.Abort()
		}
	})
	registerRiderNotificationRoutes(r, a)
	r.GET("/saved-places", a.savedPlaces)
	r.PUT("/saved-places/:label", a.putSavedPlace)
	r.DELETE("/saved-places/:label", a.deleteSavedPlace)
	r.GET("/saved-places/:label/resolve", a.resolveSavedPlace)
	r.GET("/trusted-contacts", a.contacts)
	r.PUT("/trusted-contacts/:contactID", a.putContact)
	r.DELETE("/trusted-contacts/:contactID", a.deleteContact)
	r.GET("/preferences", a.preferences)
	r.PATCH("/preferences", a.patchPreferences)
	r.POST("/data-export", a.requestExport)
	r.GET("/data-export", a.latestExport)
	r.GET("/data-export/:exportID", a.getExport)
	r.POST("/account-deletion", a.requestDeletion)
	r.GET("/account-deletion", a.getDeletion)
}
func riderAccountOwner(c *gin.Context) string { u, _ := gatewayauth.CurrentUser(c); return u.ID }
func accountError(c *gin.Context) {
	driverError(c, 503, "account_unavailable", "Account data is unavailable")
}
func validAccountText(value string, max int) bool {
	return value != "" && len([]rune(value)) <= max && !strings.ContainsFunc(value, unicode.IsControl)
}
func (a *riderAccountAPI) lockOwner(c *gin.Context) (pgx.Tx, error) {
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		return nil, err
	}
	var active bool
	err = tx.QueryRow(c.Request.Context(), `SELECT active FROM users WHERE id=$1::UUID AND roles @> ARRAY['rider']::TEXT[] FOR UPDATE`, riderAccountOwner(c)).Scan(&active)
	if err != nil || !active {
		tx.Rollback(c.Request.Context())
		return nil, errors.New("account unavailable")
	}
	return tx, nil
}
func (a *riderAccountAPI) savedPlaces(c *gin.Context) {
	var data []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'label',label,'provider',provider,'placeId',place_id,'version',version,'verifiedAt',verified_at,'updatedAt',updated_at,'metadataState','unresolved') ORDER BY label_key),'[]'::JSONB) FROM rider_saved_places WHERE rider_id=$1::UUID`, riderAccountOwner(c)).Scan(&data)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": json.RawMessage(data)}})
}
func (a *riderAccountAPI) putSavedPlace(c *gin.Context) {
	label := strings.TrimSpace(c.Param("label"))
	var body struct {
		PlaceID  string `json:"placeId"`
		Provider string `json:"provider"`
		Version  *int64 `json:"version"`
	}
	if c.ShouldBindJSON(&body) != nil || !validAccountText(label, 80) || !validAccountText(body.PlaceID, 256) || body.Version == nil || *body.Version < 0 {
		driverError(c, 400, "invalid_saved_place", "Label, resolved place ID and version are required")
		return
	}
	resolved, err := callRideAgent(c.Request.Context(), agent.Request{SchemaVersion: 1, PlaceID: body.PlaceID})
	if err != nil || resolved.State != "resolved" || resolved.Place == nil {
		driverError(c, 503, "places_unavailable", "Place resolution is unavailable; nothing was saved")
		return
	}
	if !validAccountText(resolved.Place.Provider, 40) || body.Provider != resolved.Place.Provider {
		driverError(c, 503, "places_unavailable", "Resolved provider is unavailable")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var id, oldProvider, oldPlace string
	var version int64
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT,provider,place_id,version FROM rider_saved_places WHERE rider_id=$1::UUID AND label_key=$2 FOR UPDATE`, riderAccountOwner(c), strings.ToLower(label)).Scan(&id, &oldProvider, &oldPlace, &version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		accountError(c)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if *body.Version != 0 {
			driverError(c, 409, "saved_place_changed", "Reload saved places before editing")
			return
		}
		var count int
		if tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM rider_saved_places WHERE rider_id=$1::UUID`, riderAccountOwner(c)).Scan(&count) != nil {
			accountError(c)
			return
		}
		if count >= 20 {
			driverError(c, 409, "saved_place_limit", "At most 20 saved places are supported")
			return
		}
		id = uuid.NewString()
		version = 1
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_saved_places(id,rider_id,label,label_key,provider,place_id) VALUES($1::UUID,$2::UUID,$3,$4,$5,$6)`, id, riderAccountOwner(c), label, strings.ToLower(label), resolved.Place.Provider, resolved.Place.ID)
	} else if oldProvider != resolved.Place.Provider || oldPlace != resolved.Place.ID {
		if version != *body.Version {
			driverError(c, 409, "saved_place_changed", "Reload saved places before editing")
			return
		}
		version++
		_, err = tx.Exec(c.Request.Context(), `UPDATE rider_saved_places SET provider=$3,place_id=$4,version=$5,verified_at=NOW(),updated_at=NOW() WHERE rider_id=$1::UUID AND id=$2::UUID`, riderAccountOwner(c), id, resolved.Place.Provider, resolved.Place.ID, version)
	}
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "label": label, "placeId": resolved.Place.ID, "provider": resolved.Place.Provider, "version": version, "metadataState": "resolved", "place": resolved.Place}})
}
func (a *riderAccountAPI) deleteSavedPlace(c *gin.Context) {
	label := strings.TrimSpace(c.Param("label"))
	if !validAccountText(label, 80) {
		driverError(c, 400, "invalid_saved_place", "A saved-place label is required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM rider_saved_places WHERE rider_id=$1::UUID AND label_key=$2`, riderAccountOwner(c), strings.ToLower(label)); err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"deleted": true}})
}
func (a *riderAccountAPI) resolveSavedPlace(c *gin.Context) {
	var id, provider string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT place_id,provider FROM rider_saved_places WHERE rider_id=$1::UUID AND label_key=$2`, riderAccountOwner(c), strings.ToLower(strings.TrimSpace(c.Param("label")))).Scan(&id, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "saved_place_not_found", "Saved place was not found")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	result, err := callRideAgent(c.Request.Context(), agent.Request{SchemaVersion: 1, PlaceID: id})
	if err != nil || result.State != "resolved" || result.Place == nil || result.Place.Provider != provider {
		driverError(c, 503, "places_unavailable", "Saved-place metadata is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"place": result.Place}})
}
func (a *riderAccountAPI) contacts(c *gin.Context) {
	var data []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'fullName',full_name,'relationship',relationship,'phoneNumber',phone_e164,'updatedAt',updated_at) ORDER BY id),'[]'::JSONB) FROM rider_trusted_contacts WHERE rider_id=$1::UUID`, riderAccountOwner(c)).Scan(&data)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": json.RawMessage(data)}})
}
func (a *riderAccountAPI) putContact(c *gin.Context) {
	id, err := uuid.Parse(c.Param("contactID"))
	var body struct {
		FullName     string `json:"fullName"`
		Relationship string `json:"relationship"`
		Phone        string `json:"phoneNumber"`
	}
	if err != nil || c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_contact", "A stable contact ID and contact details are required")
		return
	}
	body.FullName = strings.TrimSpace(body.FullName)
	body.Relationship = strings.TrimSpace(body.Relationship)
	phone, ok := normalizeSafetyContactPhone(body.Phone)
	if !validAccountText(body.FullName, 120) || !validAccountText(body.Relationship, 80) || !ok {
		driverError(c, 400, "invalid_contact", "Name, relationship and a valid phone number are required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var owner string
	err = tx.QueryRow(c.Request.Context(), `SELECT rider_id::TEXT FROM rider_trusted_contacts WHERE id=$1::UUID`, id).Scan(&owner)
	if err == nil && owner != riderAccountOwner(c) {
		driverError(c, 404, "contact_not_found", "Contact was not found")
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		accountError(c)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var count int
		if tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM rider_trusted_contacts WHERE rider_id=$1::UUID`, riderAccountOwner(c)).Scan(&count) != nil {
			accountError(c)
			return
		}
		if count >= 5 {
			driverError(c, 409, "contact_limit", "At most five trusted contacts are supported")
			return
		}
	}
	tag, err := tx.Exec(c.Request.Context(), `INSERT INTO rider_trusted_contacts(id,rider_id,full_name,relationship,phone_e164) VALUES($1::UUID,$2::UUID,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET full_name=EXCLUDED.full_name,relationship=EXCLUDED.relationship,phone_e164=EXCLUDED.phone_e164,updated_at=NOW() WHERE rider_trusted_contacts.rider_id=EXCLUDED.rider_id`, id, riderAccountOwner(c), body.FullName, body.Relationship, phone)
	if err == nil && tag.RowsAffected() != 1 {
		driverError(c, 404, "contact_not_found", "Contact was not found")
		return
	}
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "fullName": body.FullName, "relationship": body.Relationship, "phoneNumber": phone}})
}
func (a *riderAccountAPI) deleteContact(c *gin.Context) {
	id, err := uuid.Parse(c.Param("contactID"))
	if err != nil {
		driverError(c, 400, "invalid_contact", "Valid contact ID is required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM rider_trusted_contacts WHERE rider_id=$1::UUID AND id=$2::UUID`, riderAccountOwner(c), id); err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"deleted": true}})
}
func (a *riderAccountAPI) preferences(c *gin.Context) {
	var reduce, share bool
	err := a.pool.QueryRow(c.Request.Context(), `SELECT reduce_motion,share_ride_status FROM rider_preferences WHERE rider_id=$1::UUID`, riderAccountOwner(c)).Scan(&reduce, &share)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"reduceMotion": reduce, "shareRideStatus": share, "rideSharingEffective": false, "rideSharingUnavailableReason": "delivery_not_configured", "accessibilityHandoff": "rider_presentation_only"}})
}
func (a *riderAccountAPI) patchPreferences(c *gin.Context) {
	var body struct {
		Reduce *bool `json:"reduceMotion"`
		Share  *bool `json:"shareRideStatus"`
	}
	if c.ShouldBindJSON(&body) != nil || (body.Reduce == nil && body.Share == nil) {
		driverError(c, 400, "invalid_preferences", "A supported preference is required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_preferences(rider_id,reduce_motion,share_ride_status) VALUES($1::UUID,COALESCE($2,FALSE),COALESCE($3,FALSE)) ON CONFLICT(rider_id) DO UPDATE SET reduce_motion=COALESCE($2,rider_preferences.reduce_motion),share_ride_status=COALESCE($3,rider_preferences.share_ride_status),updated_at=NOW()`, riderAccountOwner(c), body.Reduce, body.Share)
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	a.preferences(c)
}
