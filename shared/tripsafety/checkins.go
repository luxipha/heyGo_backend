// Package tripsafety detects advisory check-ins from trusted Driver locations.
// It never claims an emergency or alerts an emergency service automatically.
package tripsafety

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"math"
	"time"
)

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func metres(a, b Point) float64 {
	lat := (a.Latitude + b.Latitude) * math.Pi / 360
	x := (a.Longitude - b.Longitude) * math.Cos(lat) * 111320
	y := (a.Latitude - b.Latitude) * 111320
	return math.Hypot(x, y)
}
func routeDistance(p Point, coordinates [][]float64) float64 {
	best := math.Inf(1)
	for i := 1; i < len(coordinates); i++ {
		a, b := coordinates[i-1], coordinates[i]
		if len(a) != 2 || len(b) != 2 {
			continue
		}
		scale := math.Cos(p.Latitude*math.Pi/180) * 111320
		ax, ay := (a[0]-p.Longitude)*scale, (a[1]-p.Latitude)*111320
		bx, by := (b[0]-p.Longitude)*scale, (b[1]-p.Latitude)*111320
		dx, dy := bx-ax, by-ay
		ratio := 0.0
		if d := dx*dx + dy*dy; d > 0 {
			ratio = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/d))
		}
		best = math.Min(best, math.Hypot(ax+ratio*dx, ay+ratio*dy))
	}
	return best
}

// Observe must execute in the same transaction as accepting the new location.
// Gaps over 90s reset observation windows; stale/out-of-order fixes cannot alert.
func Observe(ctx context.Context, tx pgx.Tx, trip string, at time.Time, lat, lng float64) error {
	if time.Since(at) > 2*time.Minute || at.After(time.Now().Add(10*time.Second)) {
		return nil
	}
	var rider, status string
	var revision int64
	var started *time.Time
	var route, stops []byte
	err := tx.QueryRow(ctx, `SELECT t.rider_id::text,t.status,t.route_revision,t.started_at,f.route,t.stops FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::uuid FOR UPDATE OF t`, trip).Scan(&rider, &status, &revision, &started, &route, &stops)
	if err != nil {
		return err
	}
	if status != "started" || started == nil || at.Sub(*started) < 3*time.Minute {
		return nil
	}
	point := Point{lat, lng}
	var path struct {
		Routes []struct {
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	_ = json.Unmarshal(route, &path)
	deviation := false
	if len(path.Routes) > 0 && len(path.Routes[0].Geometry.Coordinates) > 1 {
		distance := routeDistance(point, path.Routes[0].Geometry.Coordinates)
		deviation = !math.IsInf(distance, 0) && distance > 500
	}
	var planned []struct {
		Place struct {
			Location Point `json:"location"`
		} `json:"place"`
		CompletedAt *time.Time `json:"completedAt"`
	}
	_ = json.Unmarshal(stops, &planned)
	plannedStop := false
	for _, s := range planned {
		if s.CompletedAt == nil && metres(point, s.Place.Location) <= 150 {
			plannedStop = true
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO trip_safety_monitor(trip_id,route_revision,anchor,stationary_since,last_at) VALUES($1::uuid,$2,ST_SetSRID(ST_MakePoint($3,$4),4326)::geography,$5,$5) ON CONFLICT DO NOTHING`, trip, revision, lng, lat, at)
	if err != nil {
		return err
	}
	var oldRevision int64
	var anchor Point
	var since, last time.Time
	var offSince *time.Time
	var samples int
	err = tx.QueryRow(ctx, `SELECT route_revision,ST_Y(anchor::geometry),ST_X(anchor::geometry),stationary_since,last_at,deviation_since,deviation_samples FROM trip_safety_monitor WHERE trip_id=$1::uuid FOR UPDATE`, trip).Scan(&oldRevision, &anchor.Latitude, &anchor.Longitude, &since, &last, &offSince, &samples)
	if err != nil {
		return err
	}
	if at.Before(last) || at.Equal(last) {
		return nil
	}
	if revision != oldRevision || at.Sub(last) > 90*time.Second {
		anchor = point
		since = at
		offSince = nil
		samples = 0
	}
	if metres(anchor, point) > 60 || plannedStop {
		anchor = point
		since = at
	}
	if deviation && !plannedStop {
		if offSince == nil {
			v := at
			offSince = &v
		}
		samples++
	} else {
		offSince = nil
		samples = 0
	}
	_, err = tx.Exec(ctx, `UPDATE trip_safety_monitor SET route_revision=$2,anchor=ST_SetSRID(ST_MakePoint($3,$4),4326)::geography,stationary_since=$5,last_at=$6,deviation_since=$7,deviation_samples=$8 WHERE trip_id=$1::uuid`, trip, revision, anchor.Longitude, anchor.Latitude, since, at, offSince, samples)
	if err != nil {
		return err
	}
	kinds := []string{}
	if !plannedStop && at.Sub(since) >= 4*time.Minute {
		kinds = append(kinds, "ride_stopped")
	}
	if offSince != nil && at.Sub(*offSince) >= time.Minute && samples >= 3 {
		kinds = append(kinds, "route_deviation")
	}
	for _, kind := range kinds {
		id := uuid.NewString()
		result, err := tx.Exec(ctx, `INSERT INTO trip_safety_checkins(id,trip_id,rider_id,kind,route_revision,observed_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6) ON CONFLICT(trip_id,kind,route_revision) DO NOTHING`, id, trip, rider, kind, revision, at)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"tripId": trip, "checkinId": id, "kind": kind, "routeRevision": revision, "observedAt": at, "requiresResponse": true})
		if _, err = messaging.AppendEventTx(ctx, tx, rider, "checkin:"+id, "trip.event.safety_checkin", raw); err != nil {
			return err
		}
	}
	return nil
}

var ErrConflict = errors.New("check-in already answered or no longer current")
