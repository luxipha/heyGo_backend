package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runtime struct {
	Pool            *pgxpool.Pool
	BaseURL, Origin string
}
type liveActor struct {
	Actor
	socket        *websocket.Conn
	movement      *movement
	autoAttempted map[string]bool
	mu            sync.Mutex
}

func (r *Runtime) event(run, id, kind string, data any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, _ := json.Marshal(data)
	r.Pool.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,participant_id,kind,data)VALUES($1::uuid,$2::uuid,$3,$4::jsonb)`, run, nullable(id), kind, raw)
}
func (r *Runtime) state(run, id, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.Pool.Exec(ctx, `UPDATE simulation_participants SET runtime_status=$3,updated_at=NOW()WHERE id=$1::uuid AND simulation_run_id=$2::uuid`, id, run, status)
}
func (r *Runtime) request(ctx context.Context, a *liveActor, method, path string, body any) (map[string]any, error) {
	return r.requestAttempt(ctx, a, method, path, body, "", 0)
}
func (r *Runtime) requestKey(ctx context.Context, a *liveActor, method, path string, body any, key string) (map[string]any, error) {
	return r.requestAttempt(ctx, a, method, path, body, key, 0)
}
func (r *Runtime) requestAttempt(ctx context.Context, a *liveActor, method, path string, body any, key string, attempt int) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.BaseURL, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return nil, errors.New("invalid runtime API URL")
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return nil, errors.New("runtime API connection unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 429 && attempt < 2 {
		delay, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		if delay < 1 {
			delay = 1
		}
		if delay > 60 {
			return nil, errors.New("API rate limit exceeds retry bound")
		}
		res.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(delay) * time.Second):
		}
		return r.requestAttempt(ctx, a, method, path, body, key, attempt+1)
	}

	var reply struct {
		Data  map[string]any `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if json.Unmarshal(b, &reply) != nil {
		return nil, errors.New("invalid runtime API response")
	}
	if res.StatusCode == 429 {
		return nil, errors.New("API rate limit remains after bounded retry")
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("API HTTP %d (%s)", res.StatusCode, reply.Error.Code)
	}
	return reply.Data, nil
}
func (a *liveActor) send(kind string, data any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.socket == nil {
		return errors.New("actor socket unavailable")
	}
	a.socket.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return a.socket.WriteJSON(map[string]any{"type": kind, "data": data})
}
func (a *liveActor) location() error {
	return a.send("driver.cmd.location", map[string]any{"recordedAt": time.Now().UTC(), "location": map[string]any{"latitude": a.Config.Latitude, "longitude": a.Config.Longitude}})
}
func (r *Runtime) connect(ctx context.Context, run string, a *liveActor) error {
	if a.Config.Role != "driver" {
		r.state(run, a.ParticipantID, "idle")
		return nil
	}
	u, e := url.Parse(r.BaseURL)
	if e != nil {
		return errors.New("invalid runtime API URL")
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	} else {
		return errors.New("runtime API must use HTTP or HTTPS")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws/drivers"
	u.RawQuery = ""
	h := http.Header{}
	h.Set("Authorization", "Bearer "+a.Token)
	h.Set("Origin", r.Origin)
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	conn, _, e := dialer.DialContext(ctx, u.String(), h)
	if e != nil {
		return errors.New("Driver socket connection rejected")
	}
	a.socket = conn
	a.autoAttempted = map[string]bool{}
	r.state(run, a.ParticipantID, "connected")
	go r.read(ctx, run, a, conn)
	if e = a.location(); e != nil {
		return e
	}
	// The actual preference contract owns auto-accept. The runtime acts as its client.
	prefs, e := r.request(ctx, a, "GET", "/driver/ride-preferences", nil)
	if e != nil {
		return e
	}
	current, _ := prefs["preferences"].(map[string]any)
	if current == nil {
		return errors.New("preference response missing")
	}
	_, e = r.request(ctx, a, "PUT", "/driver/ride-preferences", map[string]any{"version": current["version"], "rideTypes": []string{}, "longDistance": true, "airport": true, "workingAreas": []string{}, "autoAccept": a.Config.AutoAccept})
	if e != nil {
		return e
	}
	if !a.Config.Online {
		r.state(run, a.ParticipantID, "offline")
		return nil
	}
	for attempt := 0; attempt < 10; attempt++ {
		_, e = r.request(ctx, a, "POST", "/driver/availability", map[string]bool{"available": true})
		if e == nil {
			r.state(run, a.ParticipantID, "online")
			r.event(run, a.ParticipantID, "availability.online", map[string]any{})
			return nil
		}
		if !strings.Contains(e.Error(), "location_required") && !strings.Contains(e.Error(), "socket_required") {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	r.state(run, a.ParticipantID, "blocked")
	r.event(run, a.ParticipantID, "availability.blocked", map[string]any{"reason": e.Error()})
	return nil
}
func (r *Runtime) read(ctx context.Context, run string, a *liveActor, conn *websocket.Conn) {
	for {
		var event struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if conn.ReadJSON(&event) != nil {
			if ctx.Err() == nil {
				r.state(run, a.ParticipantID, "disconnected")
				r.event(run, a.ParticipantID, "socket.disconnected", map[string]any{})
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if event.Type != "driver.cmd.trip_request" {
			if strings.HasPrefix(event.Type, "trip.event.") || event.Type == "driver.event.command_acknowledged" {
				r.event(run, a.ParticipantID, event.Type, event.Data)
			}
			continue
		}
		trip, _ := event.Data["trip"].(map[string]any)
		offer, _ := event.Data["offer"].(map[string]any)
		tid, _ := trip["id"].(string)
		if tid == "" {
			continue
		}
		// Match the client path: inspect the received offer, read the server preference,
		// and send the authenticated response. Do not add simulator-only database waits
		// or synchronous trace writes before the time-sensitive response.
		receivedAt := time.Now().UTC()
		if a.autoAttempted[tid] {
			continue
		}
		a.autoAttempted[tid] = true
		expires, _ := offer["expiresAt"].(string)
		deadline, _ := time.Parse(time.RFC3339Nano, expires)
		outcome := "expired_on_receipt"
		reason := ""
		preferenceMs := int64(0)
		sentRemainingMs := int64(0)
		if deadline.After(receivedAt.Add(time.Second)) {
			began := time.Now()
			prefs, e := r.request(ctx, a, "GET", "/driver/ride-preferences", nil)
			preferenceMs = time.Since(began).Milliseconds()
			if e != nil {
				outcome = "preference_unavailable"
				reason = e.Error()
			} else {
				p, _ := prefs["preferences"].(map[string]any)
				outcome = "manual_preference"
				if p["autoAccept"] == true {
					outcome = "expired_after_preference"
					if deadline.After(time.Now().Add(time.Second)) {
						if e = a.send("driver.cmd.trip_accept", map[string]any{"tripID": tid}); e != nil {
							outcome = "response_failed"
							reason = e.Error()
						} else {
							outcome = "accept_sent"
							sentRemainingMs = time.Until(deadline).Milliseconds()
						}
					}
				}
			}
		}
		r.event(run, a.ParticipantID, "offer.received", map[string]any{"tripId": tid, "offer": offer, "receivedAt": receivedAt, "receivedRemainingMs": deadline.Sub(receivedAt).Milliseconds(), "preferenceMs": preferenceMs, "sentRemainingMs": sentRemainingMs, "outcome": outcome, "reason": reason})
		switch outcome {
		case "accept_sent":
			r.event(run, a.ParticipantID, "offer.accept_sent", map[string]any{"tripId": tid, "source": "server_preference"})
		case "preference_unavailable":
			r.event(run, a.ParticipantID, "offer.preference_unavailable", map[string]any{"tripId": tid, "reason": reason, "preferenceMs": preferenceMs})
		case "response_failed":
			r.event(run, a.ParticipantID, "offer.response_failed", map[string]any{"tripId": tid, "reason": reason})
		}

	}
}

// Run is started explicitly by staff. One advisory lock owns a run across API replicas.
// Fixture sessions and runtime stop after ten minutes. Road movement and client
// concurrency are bounded; the runtime does not refresh sessions indefinitely.
func (r *Runtime) Run(run string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	lease, e := pgx.ConnectConfig(ctx, r.Pool.Config().ConnConfig.Copy())
	if e != nil {
		r.fail(run, "runtime database unavailable")
		return
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		lease.Close(closeCtx)
	}()
	var acquired bool
	e = lease.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "simulation:"+run).Scan(&acquired)
	if e != nil || !acquired {
		return
	}
	defer func() {
		unlockCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		lease.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "simulation:"+run)
	}()
	var desiredAtStart string
	if e := r.Pool.QueryRow(ctx, `SELECT status FROM simulation_runs WHERE id=$1::uuid`, run).Scan(&desiredAtStart); e != nil || desiredAtStart != "starting" {
		return
	}
	actors := map[string]*liveActor{}
	failed := false
	defer func() {
		cancel()
		var cleanupGroup sync.WaitGroup
		cleanupSlots := make(chan struct{}, 8)
		var cleanupMu sync.Mutex
		for _, a := range actors {
			a := a
			cleanupGroup.Add(1)
			go func() {
				defer cleanupGroup.Done()
				cleanupSlots <- struct{}{}
				defer func() { <-cleanupSlots }()
				cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
				if a.Config.Role == "driver" {
					_, err := r.request(cleanup, a, "POST", "/driver/availability", map[string]bool{"available": false})
					if err != nil {
						r.event(run, a.ParticipantID, "cleanup.availability_failed", map[string]any{"reason": err.Error()})
					}
				}
				if a.socket != nil {
					a.socket.Close()
				}
				_, revokeErr := r.Pool.Exec(cleanup, `UPDATE user_sessions SET revoked_at=NOW()WHERE id=$1::uuid AND user_id=$2::uuid`, a.SessionID, a.ID)
				done()
				if revokeErr != nil {
					cleanupMu.Lock()
					failed = true
					cleanupMu.Unlock()
					r.state(run, a.ParticipantID, "cleanup_failed")
					r.event(run, a.ParticipantID, "cleanup.session_revoke_failed", map[string]any{})
				} else {
					r.state(run, a.ParticipantID, "stopped")
				}
			}()
		}
		cleanupGroup.Wait()
		status := "stopped"
		if failed {
			status = "failed"
		}
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		r.Pool.Exec(cleanup, `UPDATE simulation_runs SET status=$2,revision=revision+1,updated_at=NOW()WHERE id=$1::uuid`, run, status)
		unfinished := []string{}
		rows, err := r.Pool.Query(cleanup, `SELECT t.id::text FROM trips t WHERE t.status IN ('pending','assigned','accepted','arrived','started')AND EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=$1::uuid AND(p.actor_user_id=t.assigned_driver_id OR p.actor_user_id=t.rider_id))`, run)
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					unfinished = append(unfinished, id)
				}
			}
			rows.Close()
		}
		r.Pool.Exec(cleanup, `UPDATE simulation_rider_actions SET status='failed',error='runtime_stopped; booking outcome may require trip inspection',processed_at=NOW() WHERE simulation_run_id=$1::uuid AND status IN ('queued','processing')`, run)
		r.Pool.Exec(cleanup, `UPDATE simulation_commands SET status='failed',processed_at=NOW()WHERE simulation_run_id=$1::uuid AND status='queued'`, run)
		r.event(run, "", "run."+status, map[string]any{"unfinishedTripsPreserved": true, "unfinishedTripIds": unfinished, "tripInspectionAvailable": err == nil})
	}()
	rows, e := r.Pool.Query(ctx, `SELECT id::text,configuration FROM simulation_participants WHERE simulation_run_id=$1::uuid ORDER BY created_at,id`, run)
	if e != nil {
		failed = true
		r.event(run, "", "run.error", map[string]any{"reason": "participant read failed"})
		return
	}
	type item struct {
		id string
		c  Participant
	}
	items := []item{}
	for rows.Next() {
		var id string
		var raw []byte
		if rows.Scan(&id, &raw) != nil {
			rows.Close()
			failed = true
			return
		}
		var c Participant
		if json.Unmarshal(raw, &c) != nil {
			rows.Close()
			failed = true
			return
		}
		items = append(items, item{id, c})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		failed = true
		return
	}
	// Bound provisioning and client connection concurrency independently of population size.
	type provisioned struct {
		id    string
		actor *liveActor
		err   error
	}
	results := make(chan provisioned, len(items))
	slots := make(chan struct{}, 8)
	for _, item := range items {
		item := item
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			var desired string
			if e := r.Pool.QueryRow(ctx, `SELECT status FROM simulation_runs WHERE id=$1::uuid`, run).Scan(&desired); e != nil {
				results <- provisioned{id: item.id, err: e}
				return
			}
			if desired != "starting" || ctx.Err() != nil {
				results <- provisioned{id: item.id}
				return
			}
			a, e := Provision(ctx, r.Pool, run, item.id, item.c)
			if e != nil {
				results <- provisioned{id: item.id, err: e}
				return
			}
			results <- provisioned{id: item.id, actor: &liveActor{Actor: a}}
		}()
	}
	for range items {
		result := <-results
		if result.err != nil {
			failed = true
			r.event(run, result.id, "provision.failed", map[string]any{"reason": result.err.Error()})
			continue
		}
		if result.actor == nil {
			continue
		}
		actors[result.id] = result.actor
		r.event(run, result.id, "actor.provisioned", map[string]any{"actorUserId": result.actor.ID, "syntheticEvidence": true})
	}
	if failed {
		return
	}
	var connections sync.WaitGroup
	for id, actor := range actors {
		id, actor := id, actor
		connections.Add(1)
		go func() {
			defer connections.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			var desired string
			if e := r.Pool.QueryRow(ctx, `SELECT status FROM simulation_runs WHERE id=$1::uuid`, run).Scan(&desired); e != nil || desired != "starting" {
				return
			}
			if e := r.connect(ctx, run, actor); e != nil {
				r.state(run, id, "blocked")
				r.event(run, id, "connection.failed", map[string]any{"reason": e.Error()})
			}
		}()
	}
	connections.Wait()
	started, e := r.Pool.Exec(ctx, `UPDATE simulation_runs SET status='running',updated_at=NOW(),revision=revision+1 WHERE id=$1::uuid AND status='starting'`, run)
	if e != nil {
		failed = true
		return
	}
	if started.RowsAffected() != 1 {
		return
	}
	r.event(run, "", "run.started", map[string]any{"maximumSeconds": 600, "movementConnected": true})
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var requestConcurrency int
	if e := r.Pool.QueryRow(ctx, `SELECT request_concurrency FROM simulation_runs WHERE id=$1::uuid`, run).Scan(&requestConcurrency); e != nil || requestConcurrency < 1 || requestConcurrency > 100 {
		failed = true
		return
	}
	lastGPS := time.Now()
	lastMetrics := time.Now()
	for {
		select {
		case <-ctx.Done():
			r.event(run, "", "run.time_limit", map[string]any{"maximumSeconds": 600})
			return
		case <-tick.C:
			var status string
			if r.Pool.QueryRow(ctx, `SELECT status FROM simulation_runs WHERE id=$1::uuid`, run).Scan(&status) != nil {
				failed = true
				return
			}
			if status == "stopping" || status == "stopped" || status == "failed" {
				return
			}
			if time.Since(lastGPS) > 8*time.Second {
				for _, a := range actors {
					if a.socket != nil {
						a.location()
					}
				}
				lastGPS = time.Now()
			}
			if status == "paused" {
				for _, a := range actors {
					if a.movement != nil {
						a.movement.last = time.Now()
					}
				}
				continue
			}
			r.commands(ctx, run, actors)
			r.riderCommands(ctx, run, actors, requestConcurrency)
			iterationStarted := time.Now()
			var movements sync.WaitGroup
			for _, a := range actors {
				if a.Config.Role != "driver" || !a.Config.DriveTrip {
					continue
				}
				a := a
				movements.Add(1)
				go func() { defer movements.Done(); slots <- struct{}{}; defer func() { <-slots }(); r.move(ctx, run, a) }()
			}
			movements.Wait()
			if time.Since(lastMetrics) > 15*time.Second {
				r.event(run, "", "runtime.metrics", map[string]any{"actors": len(actors), "movementIterationMs": time.Since(iterationStarted).Milliseconds(), "movementConcurrency": 8, "requestConcurrency": requestConcurrency})
				lastMetrics = time.Now()
			}
			r.Pool.Exec(ctx, `UPDATE simulation_runs SET updated_at=NOW()WHERE id=$1::uuid`, run)
		}
	}
}
func (r *Runtime) fail(run, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.Pool.Exec(ctx, `UPDATE simulation_runs SET status='failed',revision=revision+1,updated_at=NOW()WHERE id=$1::uuid`, run)
	r.event(run, "", "run.error", map[string]any{"reason": reason})
}
func (r *Runtime) commands(ctx context.Context, run string, actors map[string]*liveActor) {
	rows, e := r.Pool.Query(ctx, `SELECT id::text,participant_id::text,trip_id::text,action FROM simulation_commands WHERE simulation_run_id=$1::uuid AND status='queued' ORDER BY created_at LIMIT 20`, run)
	if e != nil {
		return
	}
	type command struct{ id, actor, trip, action string }
	list := []command{}
	for rows.Next() {
		var c command
		if rows.Scan(&c.id, &c.actor, &c.trip, &c.action) == nil {
			list = append(list, c)
		}
	}
	rows.Close()
	for _, c := range list {
		a := actors[c.actor]
		if a == nil {
			continue
		}
		kind := "driver.cmd.trip_decline"
		if c.action == "accept" {
			kind = "driver.cmd.trip_accept"
		}
		status := "sent"
		var current bool
		e = r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_assignments WHERE trip_id=$1::uuid AND driver_id=$2::uuid AND status='offered'AND expires_at>NOW())`, c.trip, a.ID).Scan(&current)
		if e != nil || !current {
			r.Pool.Exec(ctx, `UPDATE simulation_commands SET status='failed',processed_at=NOW()WHERE id=$1::uuid`, c.id)
			r.event(run, c.actor, "offer.response_failed", map[string]any{"tripId": c.trip, "reason": "offer_not_current", "commandId": c.id})
			continue
		}
		if e = a.send(kind, map[string]any{"tripID": c.trip}); e != nil {
			status = "failed"
		}
		r.Pool.Exec(ctx, `UPDATE simulation_commands SET status=$2,processed_at=NOW()WHERE id=$1::uuid AND status='queued'`, c.id, status)
		r.event(run, c.actor, "offer.response_"+status, map[string]any{"tripId": c.trip, "action": c.action, "commandId": c.id})
	}
}
