package simulation

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"math"
	"time"
)

type point struct{ Lat, Lng float64 }
type movement struct {
	trip, leg   string
	points      []point
	index       int
	position    point
	last        time.Time
	finished    bool
	commandSent bool
	failed      bool
}

func meters(a, b point) float64 {
	r := math.Pi / 180
	dl := (b.Lat - a.Lat) * r
	dn := (b.Lng - a.Lng) * r
	x := math.Sin(dl/2)*math.Sin(dl/2) + math.Cos(a.Lat*r)*math.Cos(b.Lat*r)*math.Sin(dn/2)*math.Sin(dn/2)
	return 6371000 * 2 * math.Atan2(math.Sqrt(x), math.Sqrt(math.Max(0, 1-x)))
}
func heading(a, b point) float64 {
	r := math.Pi / 180
	y := math.Sin((b.Lng-a.Lng)*r) * math.Cos(b.Lat*r)
	x := math.Cos(a.Lat*r)*math.Sin(b.Lat*r) - math.Sin(a.Lat*r)*math.Cos(b.Lat*r)*math.Cos((b.Lng-a.Lng)*r)
	return math.Mod(math.Atan2(y, x)/r+360, 360)
}
func (m *movement) advance(distance float64) point {
	for m.index < len(m.points) {
		next := m.points[m.index]
		length := meters(m.position, next)
		if length <= distance || length < .01 {
			m.position = next
			m.index++
			distance -= length
			continue
		}
		fraction := distance / length
		m.position = point{m.position.Lat + (next.Lat-m.position.Lat)*fraction, m.position.Lng + (next.Lng-m.position.Lng)*fraction}
		return m.position
	}
	m.finished = true
	return m.position
}
func (r *Runtime) movementCommand(ctx context.Context, run string, a *liveActor, m *movement, action string) error {
	if m.commandSent {
		return nil
	}
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("simulation-command:"+run+":"+a.ParticipantID+":"+m.trip+":"+action)).String()
	_, e := r.requestKey(ctx, a, "POST", "/trips/"+m.trip+"/"+action, map[string]any{}, key)
	if e != nil {
		return e
	}
	m.commandSent = true
	r.event(run, a.ParticipantID, "movement.command_queued", map[string]any{"tripId": m.trip, "action": action, "commandId": key})
	return nil
}
func (r *Runtime) move(ctx context.Context, run string, a *liveActor) {
	if !a.Config.DriveTrip || a.Config.Role != "driver" || a.socket == nil {
		return
	}
	var trip, status string
	var pickup, destination point
	var arrived *time.Time
	e := r.Pool.QueryRow(ctx, `SELECT t.id::text,t.status,ST_Y(f.pickup::geometry),ST_X(f.pickup::geometry),ST_Y(f.destination::geometry),ST_X(f.destination::geometry),t.arrived_at FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.assigned_driver_id=$1::uuid AND t.status IN ('accepted','arrived','started') ORDER BY t.updated_at DESC LIMIT 1`, a.ID).Scan(&trip, &status, &pickup.Lat, &pickup.Lng, &destination.Lat, &destination.Lng, &arrived)
	if errors.Is(e, pgx.ErrNoRows) {
		if a.movement != nil {
			r.event(run, a.ParticipantID, "movement.assignment_ended", map[string]any{"tripId": a.movement.trip})
			a.movement = nil
		}
		return
	}
	if e != nil {
		return
	}
	if status == "arrived" {
		if a.movement == nil || a.movement.trip != trip || a.movement.leg != "waiting" {
			a.movement = &movement{trip: trip, leg: "waiting"}
			r.event(run, a.ParticipantID, "movement.waiting_at_pickup", map[string]any{"tripId": trip, "waitSeconds": a.Config.WaitAtPickupSeconds})
		}
		if a.movement.failed {
			return
		}
		if arrived != nil && time.Since(*arrived) >= time.Duration(a.Config.WaitAtPickupSeconds)*time.Second {
			if e = r.movementCommand(ctx, run, a, a.movement, "start"); e != nil {
				r.movementError(run, a, e)
			}
		}
		return
	}
	leg, target := "pickup", pickup
	if status == "started" {
		leg, target = "destination", destination
	}
	if a.movement == nil || a.movement.trip != trip || a.movement.leg != leg {
		if meters(point{a.Config.Latitude, a.Config.Longitude}, target) < 5 {
			a.movement = &movement{trip: trip, leg: leg, points: []point{target}, position: target, finished: true}
			r.event(run, a.ParticipantID, "movement.already_at_endpoint", map[string]any{"tripId": trip, "leg": leg})
			return
		}
		// Route through the same authenticated Driver endpoint, not a simulator router.
		route, e := r.request(ctx, a, "POST", "/routes/driver", map[string]any{"tripId": trip, "origin": map[string]float64{"latitude": a.Config.Latitude, "longitude": a.Config.Longitude}, "destination": map[string]float64{"latitude": target.Lat, "longitude": target.Lng}})
		if e != nil {
			a.movement = &movement{trip: trip, leg: leg, failed: true}
			r.movementError(run, a, e)
			return
		}
		raw, ok := route["coordinates"].([]any)
		if !ok || len(raw) < 2 || len(raw) > 5000 {
			a.movement = &movement{trip: trip, leg: leg, failed: true}
			r.movementError(run, a, errors.New("route geometry unavailable or exceeds 5000 points"))
			return
		}
		points := make([]point, 0, len(raw))
		for _, v := range raw {
			xy, ok := v.([]any)
			if !ok || len(xy) != 2 {
				return
			}
			lng, lok := xy[0].(float64)
			lat, aok := xy[1].(float64)
			if !lok || !aok || math.Abs(lat) > 90 || math.Abs(lng) > 180 {
				return
			}
			points = append(points, point{lat, lng})
		}
		if meters(points[len(points)-1], target) > 150 {
			a.movement = &movement{trip: trip, leg: leg, failed: true}
			r.movementError(run, a, errors.New("road endpoint is over 150m from requested destination; no teleport performed"))
			return
		}
		a.movement = &movement{trip: trip, leg: leg, points: points, index: 1, position: points[0], last: time.Now()}
		r.event(run, a.ParticipantID, "movement.route_ready", map[string]any{"tripId": trip, "leg": leg, "coordinates": raw, "provider": route["provider"], "speedMetersPerSecond": a.Config.MovementSpeedMPS, "acceleratedMovementOnly": true})
	}
	m := a.movement
	if m.failed {
		return
	}
	if m.finished {
		var closeEnough bool
		e = r.Pool.QueryRow(ctx, `SELECT ST_DWithin(location,ST_SetSRID(ST_MakePoint($2,$3),4326)::geography,30)FROM driver_live_locations WHERE driver_id=$1::uuid`, a.ID, m.position.Lng, m.position.Lat).Scan(&closeEnough)
		if e != nil || !closeEnough {
			return
		}
		action := "arrival"
		if leg == "destination" {
			action = "complete"
		}
		if e = r.movementCommand(ctx, run, a, m, action); e != nil {
			r.movementError(run, a, e)
		}
		return
	}
	now := time.Now()
	seconds := math.Min(5, now.Sub(m.last).Seconds())
	m.last = now
	before := m.position
	p := m.advance(a.Config.MovementSpeedMPS * seconds)
	a.Config.Latitude, a.Config.Longitude = p.Lat, p.Lng
	if e = a.send("driver.cmd.location", map[string]any{"recordedAt": now.UTC(), "location": map[string]float64{"latitude": p.Lat, "longitude": p.Lng, "heading": heading(before, p), "speed": a.Config.MovementSpeedMPS}}); e != nil {
		r.movementError(run, a, e)
	}
}
func (r *Runtime) movementError(run string, a *liveActor, e error) {
	if a.movement != nil {
		a.movement.failed = true
	}
	r.event(run, a.ParticipantID, "movement.blocked", map[string]any{"reason": e.Error()})
}
