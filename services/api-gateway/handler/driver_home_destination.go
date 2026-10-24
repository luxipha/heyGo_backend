package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"net/http"
	"strings"
)

type driverHomeDestination struct {
	ResidentialAddress string `json:"residentialAddress"`
	PlaceID            string `json:"placeId"`
	Name               string `json:"name"`
	Address            string `json:"address"`
	Enabled            bool   `json:"enabled"`
	Version            int64  `json:"version"`
}

func (a *driverAPI) homeDestination(c *gin.Context) {
	var home driverHomeDestination
	err := a.pool.QueryRow(c.Request.Context(), `SELECT place_id,name,address,enabled,version FROM driver_home_destinations WHERE driver_id=$1::uuid`, driverID(c)).Scan(&home.PlaceID, &home.Name, &home.Address, &home.Enabled, &home.Version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 503, "home_unavailable", "Home destination could not be loaded")
		return
	}
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT residential_address FROM driver_identity_profiles WHERE driver_id=$1::uuid),'')`, driverID(c)).Scan(&home.ResidentialAddress); err != nil {
		driverError(c, 503, "home_unavailable", "Home destination could not be loaded")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: home})
}
func (a *driverAPI) resolveHomeDestination(c *gin.Context) {
	status, err := geo.ReadSettings(c.Request.Context(), a.pool)
	if err != nil || status.Settings.PlaceProvider != "google" {
		driverError(c, 503, "places_unavailable", "Address search is unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var request agent.Request
	if c.ShouldBindJSON(&request) != nil || request.SchemaVersion != 1 || len(request.Message) > 160 || len(request.PlaceID) > 256 {
		driverError(c, 400, "invalid_home_search", "Enter a home address")
		return
	}
	reply, err := callRideAgent(c.Request.Context(), request)
	if err != nil {
		driverError(c, 503, "places_unavailable", "Address search is unavailable. Try again")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: reply})
}
func (a *driverAPI) saveHomeDestination(c *gin.Context) {
	var body struct {
		PlaceID string `json:"placeId"`
		Version int64  `json:"version"`
		Enabled bool   `json:"enabled"`
	}
	if c.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.PlaceID) == "" || len(body.PlaceID) > 256 || body.Version < 0 {
		driverError(c, 400, "invalid_home", "Select and confirm a home address")
		return
	}
	status, err := geo.ReadSettings(c.Request.Context(), a.pool)
	if err != nil || status.Settings.PlaceProvider != "google" {
		driverError(c, 503, "places_unavailable", "Address confirmation is unavailable")
		return
	}
	reply, err := callRideAgent(c.Request.Context(), agent.Request{SchemaVersion: 1, PlaceID: body.PlaceID})
	if err != nil || reply.State != "resolved" || reply.Place == nil || reply.Place.ID != body.PlaceID || len(reply.Place.Name) > 300 || len(reply.Place.Address) > 1000 || !agent.ValidLocation(reply.Place.Location) {
		driverError(c, 503, "home_unconfirmed", "Home address could not be confirmed. Try again")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var owner string
	if err = tx.QueryRow(c.Request.Context(), `SELECT id::text FROM users WHERE id=$1::uuid FOR UPDATE`, driverID(c)).Scan(&owner); err != nil {
		reviewFailure(c)
		return
	}
	var version int64
	err = tx.QueryRow(c.Request.Context(), `SELECT version FROM driver_home_destinations WHERE driver_id=$1::uuid FOR UPDATE`, owner).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		reviewFailure(c)
		return
	}
	if body.Version != version {
		driverError(c, 409, "home_conflict", "Home destination changed. Reload and try again")
		return
	}
	place := reply.Place
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_home_destinations(driver_id,place_id,name,address,location,enabled) VALUES($1::uuid,$2,$3,$4,ST_SetSRID(ST_MakePoint($6,$5),4326)::geography,$7) ON CONFLICT(driver_id) DO UPDATE SET place_id=EXCLUDED.place_id,name=EXCLUDED.name,address=EXCLUDED.address,location=EXCLUDED.location,enabled=EXCLUDED.enabled,version=driver_home_destinations.version+1,updated_at=NOW()`, owner, place.ID, place.Name, place.Address, place.Location.Latitude, place.Location.Longitude, body.Enabled)
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	a.homeDestination(c)
}
func (a *driverAPI) toggleHomeDestination(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil || body.Version < 1 {
		driverError(c, 400, "invalid_home", "Save a home destination first")
		return
	}
	result, err := a.pool.Exec(c.Request.Context(), `UPDATE driver_home_destinations SET enabled=$2,version=version+1,updated_at=NOW() WHERE driver_id=$1::uuid AND version=$3`, driverID(c), *body.Enabled, body.Version)
	if err != nil {
		reviewFailure(c)
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "home_conflict", "Home destination changed. Reload and try again")
		return
	}
	a.homeDestination(c)
}
func (a *driverAPI) toggleAutoAccept(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil || body.Version < 0 {
		driverError(c, 400, "invalid_preferences", "Choose an Auto preference")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var owner string
	if err = tx.QueryRow(c.Request.Context(), `SELECT id::text FROM users WHERE id=$1::uuid FOR UPDATE`, driverID(c)).Scan(&owner); err != nil {
		reviewFailure(c)
		return
	}
	var version int64
	err = tx.QueryRow(c.Request.Context(), `SELECT version FROM driver_ride_preferences WHERE driver_id=$1::uuid`, owner).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		reviewFailure(c)
		return
	}
	if version != body.Version {
		driverError(c, 409, "preferences_conflict", "Ride preferences changed. Reload and try again")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_ride_preferences(driver_id,auto_accept) VALUES($1::uuid,$2) ON CONFLICT(driver_id) DO UPDATE SET auto_accept=EXCLUDED.auto_accept,version=driver_ride_preferences.version+1,updated_at=NOW()`, owner, *body.Enabled)
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	a.ridePreferences(c)
}
