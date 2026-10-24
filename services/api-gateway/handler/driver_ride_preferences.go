package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
)

type ridePreferences struct {
	RideTypes    []string `json:"rideTypes"`
	LongDistance bool     `json:"longDistance"`
	Airport      bool     `json:"airport"`
	AutoAccept   bool     `json:"autoAccept"`
	WorkingAreas []string `json:"workingAreas"`
	Version      int64    `json:"version"`
}

func (a *driverAPI) ridePreferences(c *gin.Context) {
	p := ridePreferences{RideTypes: []string{}, WorkingAreas: []string{}, LongDistance: true, Airport: true, AutoAccept: true}
	err := a.pool.QueryRow(c.Request.Context(), `SELECT ride_types,long_distance,airport,auto_accept,working_areas,version FROM driver_ride_preferences WHERE driver_id=$1::UUID`, driverID(c)).Scan(&p.RideTypes, &p.LongDistance, &p.Airport, &p.AutoAccept, &p.WorkingAreas, &p.Version)
	if err != nil && err != pgx.ErrNoRows {
		driverError(c, 503, "preferences_unavailable", "Ride preferences are unavailable")
		return
	}
	types := []string{}
	labels := map[string]string{}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT slug,name FROM ride_categories WHERE driver_category_eligible($1::UUID,slug) ORDER BY name`, driverID(c))
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride types are unavailable")
		return
	}
	for rows.Next() {
		var slug, name string
		if err = rows.Scan(&slug, &name); err != nil {
			break
		}
		types = append(types, slug)
		labels[slug] = name
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride types are unavailable")
		return
	}
	areas := []gin.H{}
	rows, err = a.pool.Query(c.Request.Context(), `SELECT DISTINCT code,name FROM trip_geofences WHERE kind='region' AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()) ORDER BY name,code`)
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Working areas are unavailable")
		return
	}
	for rows.Next() {
		var code, name string
		if err = rows.Scan(&code, &name); err != nil {
			break
		}
		areas = append(areas, gin.H{"code": code, "name": name})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Working areas are unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"preferences": p, "rideTypes": types, "rideTypeLabels": labels, "workingAreas": areas}})
}
func (a *driverAPI) putRidePreferences(c *gin.Context) {
	var p ridePreferences
	if c.ShouldBindJSON(&p) != nil || p.RideTypes == nil || p.WorkingAreas == nil || len(p.RideTypes) > 10 || len(p.WorkingAreas) > 50 || p.Version < 0 {
		driverError(c, 400, "invalid_preferences", "Supply valid ride preferences")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	// Serialize creation and updates per owner. No client may edit another owner.
	var owner string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM users WHERE id=$1::UUID FOR UPDATE`, driverID(c)).Scan(&owner)
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be saved")
		return
	}
	var valid bool
	err = tx.QueryRow(c.Request.Context(), `SELECT NOT EXISTS(SELECT 1 FROM unnest($2::TEXT[]) s WHERE NOT driver_category_eligible($1::UUID,s)) AND NOT EXISTS(SELECT 1 FROM unnest($3::TEXT[]) code_value WHERE NOT EXISTS(SELECT 1 FROM trip_geofences g WHERE g.kind='region' AND g.code=code_value AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW())))`, owner, p.RideTypes, p.WorkingAreas).Scan(&valid)
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be validated")
		return
	}
	if !valid {
		driverError(c, 400, "invalid_preferences", "Select an approved vehicle category and available working areas")
		return
	}
	var version int64
	err = tx.QueryRow(c.Request.Context(), `SELECT version FROM driver_ride_preferences WHERE driver_id=$1::UUID`, owner).Scan(&version)
	if err != nil && err != pgx.ErrNoRows {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be saved")
		return
	}
	if p.Version != version {
		driverError(c, http.StatusConflict, "preferences_conflict", "Ride preferences changed. Reload and try again")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_ride_preferences(driver_id,ride_types,long_distance,airport,auto_accept,working_areas) VALUES($1::UUID,$2,$3,$4,$5,$6) ON CONFLICT(driver_id) DO UPDATE SET ride_types=EXCLUDED.ride_types,long_distance=EXCLUDED.long_distance,airport=EXCLUDED.airport,auto_accept=EXCLUDED.auto_accept,working_areas=EXCLUDED.working_areas,version=driver_ride_preferences.version+1,updated_at=NOW()`, owner, p.RideTypes, p.LongDistance, p.Airport, p.AutoAccept, p.WorkingAreas)
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be saved")
		return
	}
	if tx.Commit(c.Request.Context()) != nil {
		driverError(c, 503, "preferences_unavailable", "Ride preferences could not be saved")
		return
	}
	a.ridePreferences(c)
}
