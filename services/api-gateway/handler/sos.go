package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
)

type sosLocation struct {
	Latitude   *float64   `json:"latitude"`
	Longitude  *float64   `json:"longitude"`
	CapturedAt *time.Time `json:"capturedAt"`
}

func (a *supportAPI) createSOS(ctx *gin.Context) {
	reporter, audience, ok := supportActor(ctx)
	if !ok {
		return
	}
	tripID, err := uuid.Parse(ctx.Param("tripID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid Trip ID is required")
		return
	}
	var body struct {
		ClientRequestID string       `json:"clientRequestId"`
		Location        *sosLocation `json:"location"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_sos", "Valid SOS request is required")
		return
	}
	requestID, err := uuid.Parse(body.ClientRequestID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_sos", "clientRequestId must be a UUID")
		return
	}
	var latitude, longitude any
	var recordedAt *time.Time
	source := "unavailable"
	if body.Location != nil {
		loc := body.Location
		if loc.Latitude == nil || loc.Longitude == nil || loc.CapturedAt == nil ||
			math.IsNaN(*loc.Latitude) || math.IsNaN(*loc.Longitude) || math.IsInf(*loc.Latitude, 0) || math.IsInf(*loc.Longitude, 0) ||
			*loc.Latitude < -90 || *loc.Latitude > 90 || *loc.Longitude < -180 || *loc.Longitude > 180 ||
			loc.CapturedAt.Before(time.Now().Add(-2*time.Minute)) || loc.CapturedAt.After(time.Now().Add(30*time.Second)) {
			driverError(ctx, http.StatusBadRequest, "invalid_sos_location", "Location must include valid recent coordinates and capture time")
			return
		}
		latitude, longitude, recordedAt, source = *loc.Latitude, *loc.Longitude, loc.CapturedAt, "reporter"
	}
	// A retry must return the first intake even if the Trip has since ended.
	var existingIncident, existingCase string
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT s.incident_id::TEXT,i.case_id::TEXT FROM sos_intakes s JOIN safety_incidents i ON i.id=s.incident_id WHERE s.reporter_id=$1::UUID AND s.client_request_id=$2`, reporter, requestID).Scan(&existingIncident, &existingCase)
	if err == nil {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"incidentId": existingIncident, "caseId": existingCase, "status": "received"}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var driverID string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT assigned_driver_id::TEXT FROM trips WHERE id=$1 AND status IN ('assigned','accepted','arrived','started')
		AND (($2='rider' AND rider_id=$3::UUID) OR ($2='driver' AND assigned_driver_id=$3::UUID)) FOR UPDATE`, tripID, audience, reporter).Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "sos_trip_unavailable", "SOS requires an active linked Trip")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
		return
	}
	if source == "unavailable" {
		var lat, lon float64
		var at time.Time
		err = tx.QueryRow(ctx.Request.Context(), `SELECT ST_Y(location::geometry),ST_X(location::geometry),recorded_at FROM driver_live_locations
			WHERE driver_id=$1::UUID AND recorded_at>=NOW()-INTERVAL '30 seconds'`, driverID).Scan(&lat, &lon, &at)
		if err == nil {
			latitude, longitude, recordedAt, source = lat, lon, &at, "driver_fallback"
		} else if !errors.Is(err, pgx.ErrNoRows) {
			driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
			return
		}
	}
	caseID := uuid.New()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,subject,priority)
		VALUES($1,$2::UUID,$3,'safety',$4,'SOS requested','high')`, caseID, reporter, audience, tripID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
		return
	}
	var incidentID string
	err = tx.QueryRow(ctx.Request.Context(), `UPDATE safety_incidents SET severity='high',updated_at=NOW() WHERE case_id=$1 RETURNING id::TEXT`, caseID).Scan(&incidentID)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO sos_intakes(id,incident_id,trip_id,reporter_id,client_request_id,location,location_source,location_recorded_at)
			VALUES($1,$2::UUID,$3,$4::UUID,$5,CASE WHEN $6::FLOAT8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($7::FLOAT8,$6::FLOAT8),4326)::geography END,$8,$9)`, uuid.New(), incidentID, tripID, reporter, requestID, latitude, longitude, source, recordedAt)
	}
	if err == nil {
		payload, marshalErr := json.Marshal(gin.H{"incidentId": incidentID, "caseId": caseID.String(), "tripId": tripID.String(), "priority": "high"})
		err = marshalErr
		if err == nil {
			_, err = messaging.AppendEventTx(ctx.Request.Context(), tx, reporter, "sos-intake:"+requestID.String(), "safety.sos.created", payload)
		}
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS could not be recorded")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"incidentId": incidentID, "caseId": caseID.String(), "status": "received"}})
}

func (a *adminAPI) safetySOSEvidence(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("incidentID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_incident_id", "Valid incident ID is required")
		return
	}
	var intakeID, tripID, reporterID, source string
	var created time.Time
	var lat, lon *float64
	var recordedAt, purgeAt *time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,trip_id::TEXT,reporter_id::TEXT,location_source,
		CASE WHEN location_purge_at IS NULL OR location_purge_at>NOW() THEN ST_Y(location::geometry) END,
		CASE WHEN location_purge_at IS NULL OR location_purge_at>NOW() THEN ST_X(location::geometry) END,
		CASE WHEN location_purge_at IS NULL OR location_purge_at>NOW() THEN location_recorded_at END,
		location_purge_at,created_at FROM sos_intakes WHERE incident_id=$1`, id).Scan(&intakeID, &tripID, &reporterID, &source, &lat, &lon, &recordedAt, &purgeAt, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "sos_not_found", "SOS evidence was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "sos_unavailable", "SOS evidence is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": intakeID, "incidentId": id.String(), "tripId": tripID, "reporterId": reporterID, "locationSource": source, "latitude": lat, "longitude": lon, "locationRecordedAt": recordedAt, "locationPurgeAt": purgeAt, "createdAt": created}})
}

func RunSOSLocationPurgeOnce(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `UPDATE sos_intakes SET location=NULL,location_recorded_at=NULL
		WHERE id IN (SELECT id FROM sos_intakes WHERE location IS NOT NULL AND location_purge_at<=NOW() ORDER BY location_purge_at LIMIT 500 FOR UPDATE SKIP LOCKED)`)
	return err
}

func StartSOSLocationPurge(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			jobCtx, cancel := context.WithTimeout(ctx, time.Minute)
			if err := RunSOSLocationPurgeOnce(jobCtx, pool); err != nil && ctx.Err() == nil {
				logs.L().Warnw("SOS location purge failed", "error", err)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
