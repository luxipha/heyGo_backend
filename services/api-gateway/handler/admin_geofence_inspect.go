package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/zonepricing"
	"math"
	"strconv"
	"time"
)

// Read-only inspection uses the same ST_Covers and effective-period predicates as trip classification.
func (a *adminAPI) inspectGeofencePoint(c *gin.Context) {
	lat, e1 := strconv.ParseFloat(c.Query("latitude"), 64)
	lng, e2 := strconv.ParseFloat(c.Query("longitude"), 64)
	if e1 != nil || e2 != nil || math.IsNaN(lat) || math.IsNaN(lng) || math.IsInf(lat, 0) || math.IsInf(lng, 0) || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		driverError(c, 400, "invalid_coordinate", "Valid latitude and longitude required")
		return
	}
	at := time.Now().UTC()
	tx, err := a.pool.BeginTx(c.Request.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	rows, err := tx.Query(c.Request.Context(), `SELECT id::text,kind,code,name,version,status,
 status='approved' AND effective_from<=$3 AND (effective_until IS NULL OR effective_until>$3),
 ST_Touches(boundary,ST_SetSRID(ST_MakePoint($1,$2),4326))
 FROM trip_geofences WHERE ST_Covers(boundary,ST_SetSRID(ST_MakePoint($1,$2),4326)) ORDER BY kind,code,version LIMIT 201`, lng, lat, at)
	if err != nil {
		reviewFailure(c)
		return
	}
	matches := []gin.H{}
	current := map[string][]string{"market": {}, "region": {}, "airport": {}}
	for rows.Next() {
		var id, kind, code, name, status string
		var version int
		var effective, onBoundary bool
		if err = rows.Scan(&id, &kind, &code, &name, &version, &status, &effective, &onBoundary); err != nil {
			rows.Close()
			reviewFailure(c)
			return
		}
		matches = append(matches, gin.H{"id": id, "kind": kind, "code": code, "name": name, "version": version, "status": status, "effective": effective, "onBoundary": onBoundary})
		if effective {
			current[kind] = append(current[kind], code)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		reviewFailure(c)
		return
	}
	if len(matches) > 200 {
		driverError(c, 409, "inspection_limit", "Too many overlapping boundary versions to inspect")
		return
	}
	classification := gin.H{}
	ambiguous := []string{}
	for _, kind := range []string{"market", "region", "airport"} {
		classification[kind] = nil
		if len(current[kind]) == 1 {
			classification[kind] = current[kind][0]
		}
		if len(current[kind]) > 1 {
			ambiguous = append(ambiguous, kind)
		}
	}
	classification["ambiguousKinds"] = ambiguous
	classification["complete"] = len(current["market"]) == 1 && len(current["region"]) == 1 && len(current["airport"]) <= 1
	pricing := gin.H{"status": "unavailable", "zoneAdjustment": nil}
	if len(current["market"]) == 1 {
		rows, err = tx.Query(c.Request.Context(), `SELECT id::text,version FROM pricing_versions WHERE market_code=$1 AND status='approved' AND effective_from<=$2 AND (effective_until IS NULL OR effective_until>$2) LIMIT 2`, current["market"][0], at)
		if err != nil {
			reviewFailure(c)
			return
		}
		ids := []string{}
		version := 0
		for rows.Next() {
			var id string
			var v int
			if err = rows.Scan(&id, &v); err != nil {
				rows.Close()
				reviewFailure(c)
				return
			}
			ids = append(ids, id)
			version = v
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			reviewFailure(c)
			return
		}
		if len(ids) > 1 {
			pricing["status"] = "ambiguous"
		} else if len(ids) == 1 {
			pricing = gin.H{"status": "available", "id": ids[0], "version": version, "marketCode": current["market"][0], "zoneAdjustment": nil}
			// Query frozen pricing geometries in this same snapshot, preserving ambiguity rather than guessing.
			zr, err := tx.Query(c.Request.Context(), `SELECT zone_id::text,zone_name,kind,value FROM pricing_zone_rules WHERE pricing_id=$1::uuid AND ST_Covers(boundary,ST_SetSRID(ST_MakePoint($2,$3),4326))`, ids[0], lng, lat)
			if err != nil {
				reviewFailure(c)
				return
			}
			rules := []zonepricing.Rule{}
			for zr.Next() {
				r := zonepricing.Rule{}
				if err = zr.Scan(&r.ZoneID, &r.ZoneName, &r.Kind, &r.Value); err != nil {
					zr.Close()
					reviewFailure(c)
					return
				}
				rules = append(rules, r)
			}
			err = zr.Err()
			zr.Close()
			if err != nil {
				reviewFailure(c)
				return
			}
			if len(rules) > 1 {
				pricing["status"] = "ambiguous"
			} else if len(rules) == 1 {
				pricing["zoneAdjustment"] = rules[0]
			}
		}
	} else if len(current["market"]) > 1 {
		pricing["status"] = "ambiguous"
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"latitude": lat, "longitude": lng, "inspectedAt": at, "matches": matches, "classification": classification, "pricing": pricing}})
}
