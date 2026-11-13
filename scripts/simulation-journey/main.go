// simulation-journey exercises authenticated APIs against the named local fixture database.
// Bootstrap sessions are short-lived synthetic fixture sessions, not an OTP/provider test.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

const prefix = "heygo-simulation:v1:"
const base = "http://127.0.0.1:8080"

type object = map[string]any
type actor struct {
	id, token, session string
	socket             *websocket.Conn
	mu                 sync.Mutex
}

func fixtureID(label string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(prefix+label)).String()
}
func guard() error {
	u, e := url.Parse(os.Getenv("DATABASE_URL"))
	if e != nil || u.Hostname() != "127.0.0.1" || u.Port() != "55439" || u.Path != "/heygo_rider_staging" || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errors.New("requires named isolated local database")
	}
	for k := range u.Query() {
		if k != "sslmode" && k != "connect_timeout" {
			return errors.New("connection overrides forbidden")
		}
	}
	if os.Getenv("GCP_PROJECT_ID") != "heygo-admin-local" || os.Getenv("PUBSUB_EMULATOR_HOST") != "127.0.0.1:8086" {
		return errors.New("requires local messaging emulator")
	}
	return nil
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("entropy unavailable")
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func request(a *actor, method, path string, body any, key string) (object, error) {
	return requestAttempt(a, method, path, body, key, 0)
}
func requestAttempt(a *actor, method, path string, body any, key string, attempt int) (object, error) {
	raw, _ := json.Marshal(body)
	r, e := http.NewRequest(method, base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	c := http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := c.Do(r)
	if e != nil {
		return nil, errors.New("local API connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests && attempt < 2 {
		delay, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		if delay < 1 {
			delay = 1
		}
		if delay > 60 {
			return nil, errors.New("rate limit retry exceeds local test bound")
		}
		res.Body.Close()
		fmt.Println("respecting API Retry-After:", delay, "seconds")
		time.Sleep(time.Duration(delay) * time.Second)
		return requestAttempt(a, method, path, body, key, attempt+1)
	}

	b, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		return nil, e
	}
	var out object
	if json.Unmarshal(b, &out) != nil {
		return nil, fmt.Errorf("%s returned invalid JSON (%d)", path, res.StatusCode)
	}
	if res.StatusCode >= 300 {
		code := "unknown"
		if x, ok := out["error"].(map[string]any); ok {
			code, _ = x["code"].(string)
		}
		return nil, fmt.Errorf("%s HTTP %d code=%s", path, res.StatusCode, code)
	}
	data, ok := out["data"].(map[string]any)
	if !ok {
		return out, nil
	}
	return data, nil
}
func dial(path string, h http.Header) (*websocket.Conn, *http.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		d := *websocket.DefaultDialer
		d.HandshakeTimeout = 10 * time.Second
		conn, res, e := d.Dial("ws://127.0.0.1:8080"+path, h)
		if e == nil {
			return conn, res, nil
		}
		if res == nil || res.StatusCode != 429 {
			return nil, res, errors.New("local socket rejected")
		}
		delay, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		if delay < 1 {
			delay = 1
		}
		if delay > 60 {
			return nil, res, errors.New("socket rate limit beyond test bound")
		}
		res.Body.Close()
		fmt.Println("respecting socket Retry-After:", delay, "seconds")
		time.Sleep(time.Duration(delay) * time.Second)
	}
	return nil, nil, errors.New("socket rate limit retry exhausted")
}
func (a *actor) send(kind string, data any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.socket.WriteJSON(object{"type": kind, "data": data})
}
func (a *actor) location(lat, lng float64) error {
	return a.locationHeading(lat, lng, 0)
}
func (a *actor) locationHeading(lat, lng, heading float64) error {
	return a.send("driver.cmd.location", object{"recordedAt": time.Now().UTC(), "location": object{"latitude": lat, "longitude": lng, "heading": heading}})
}
func bearing(lat, lng, nextLat, nextLng float64) float64 {
	r := math.Pi / 180
	y := math.Sin((nextLng-lng)*r) * math.Cos(nextLat*r)
	x := math.Cos(lat*r)*math.Sin(nextLat*r) - math.Sin(lat*r)*math.Cos(nextLat*r)*math.Cos((nextLng-lng)*r)
	return math.Mod(math.Atan2(y, x)/r+360, 360)
}
func provision(ctx context.Context, p *pgxpool.Pool, label string) (*actor, error) {
	id := fixtureID(label)
	var owned bool
	e := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid AND casper_id_user_id=$2 AND active)`, id, prefix+label).Scan(&owned)
	if e != nil || !owned {
		return nil, errors.New("synthetic account missing or changed")
	}
	a := &actor{id: id, token: token(), session: uuid.NewString()}
	_, e = p.Exec(ctx, `INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name) VALUES($1::uuid,$2::uuid,$3,$4,NOW()+INTERVAL '15 minutes',NOW()+INTERVAL '16 minutes','Local journey simulator')`, a.session, id, hash(a.token), hash(token()))
	return a, e
}
func waitTrip(a *actor, id, status string) (object, error) {
	until := time.Now().Add(50 * time.Second)
	for time.Now().Before(until) {
		v, e := request(a, "GET", "/rider/trips/"+id, nil, "")
		if e != nil {
			return nil, e
		}
		if v["status"] == status {
			fmt.Println("snapshot:", status)
			return v, nil
		}
		if v["status"] == "cancelled" {
			return nil, errors.New("trip cancelled before target stage")
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("timed out waiting for %s", status)
}
func main() {
	verify := flag.Bool("verify-latest", false, "verify the latest synthetic completed trip receipt/history")
	run := flag.Bool("run", false, "execute local synthetic booking; otherwise validate guards only")
	fleet := flag.Int("drivers", 25, "number of fixture Drivers (1-25)")
	scenario := flag.String("scenario", "complete", "complete, cancel, decline, reconnect, stale-gps or no-driver")
	flag.Parse()
	if e := guard(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if *fleet < 1 || *fleet > 25 || (*scenario != "complete" && *scenario != "cancel" && *scenario != "decline" && *scenario != "reconnect" && *scenario != "stale-gps" && *scenario != "no-driver") {
		fmt.Fprintln(os.Stderr, "invalid scenario options")
		os.Exit(1)
	}
	if *verify {
		if e := verifyLatest(); e != nil {
			fmt.Fprintln(os.Stderr, "Receipt/history verification failed:", e)
			os.Exit(1)
		}
		return
	}
	if !*run {
		fmt.Println("local guard passed; use --run to create a synthetic trip")
		return
	}
	if e := journey(*fleet, *scenario); e != nil {
		fmt.Fprintln(os.Stderr, "Simulation failed:", e)
		os.Exit(1)
	}
}
func journey(n int, scenario string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return errors.New("database unavailable")
	}
	defer p.Close()
	// Each run has a distinct synthetic Rider: pending direct settlement from an earlier
	// completed test must not be cleared or presented as paid to make a rerun pass.
	riderLabel := "rider-" + uuid.NewString()
	email := riderLabel + "@simulation.heygo.invalid"
	id := fixtureID(riderLabel)
	var collision bool
	e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE (id=$1::uuid OR casper_id_user_id=$2 OR email=$3) AND NOT(id=$1::uuid AND casper_id_user_id=$2 AND email=$3))`, id, prefix+riderLabel, email).Scan(&collision)
	if e != nil {
		return e
	}
	if collision {
		return errors.New("synthetic Rider collision")
	}
	_, e = p.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,email,email_verified,active) VALUES($1::uuid,$2,$2,ARRAY['rider'],TRUE,$3,TRUE,TRUE) ON CONFLICT(id) DO NOTHING`, id, prefix+riderLabel, email)
	if e != nil {
		return e
	}
	rider, e := provision(ctx, p, riderLabel)
	if e != nil {
		return e
	}
	actors := []*actor{rider}
	defer func() {
		for _, a := range actors {
			if a.socket != nil {
				if a != rider {
					request(a, "POST", "/driver/availability", object{"available": false}, "")
				}
				a.socket.Close()
			}
			p.Exec(context.Background(), `UPDATE user_sessions SET revoked_at=NOW() WHERE id=$1::uuid`, a.session)
		}
	}()
	var existing bool
	e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trips WHERE rider_id=$1::uuid AND status IN ('pending','assigned','accepted','arrived','started'))`, rider.id).Scan(&existing)
	if e != nil {
		return e
	}
	if existing {
		return errors.New("synthetic Rider already has active trip; recover it instead of resetting")
	}
	type offer struct {
		a    *actor
		trip string
	}
	offers := make(chan offer, 64)
	stop := make(chan struct{})
	defer close(stop)
	for k := 1; k <= n; k++ {
		a, e := provision(ctx, p, fmt.Sprintf("driver-%02d", k))
		if e != nil {
			return e
		}
		actors = append(actors, a)
		h := http.Header{}
		h.Set("Origin", "http://localhost:4001")
		h.Set("Authorization", "Bearer "+a.token)
		ws, _, e := dial("/ws/drivers", h)
		if e != nil {
			return errors.New("Driver socket rejected")
		}
		a.socket = ws
		go func(a *actor, conn *websocket.Conn) {
			for {
				var event object
				if conn.ReadJSON(&event) != nil {
					return
				}
				if event["type"] == "driver.cmd.trip_request" {
					data, _ := event["data"].(map[string]any)
					trip, _ := data["trip"].(map[string]any)
					tid, _ := trip["id"].(string)
					if tid != "" {
						select {
						case offers <- offer{a, tid}:
						case <-stop:
							return
						}
					}
				}
			}
		}(a, ws)
		if e = a.location(6.428+float64(k)*.00001, 3.421); e != nil {
			return e
		}
		// Fresh GPS must be persisted before unchanged Go Online checks run.
		for attempt := 0; attempt < 20; attempt++ {
			_, e = request(a, "POST", "/driver/availability", object{"available": true}, "")
			if e == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if e != nil {
			return e
		}
	}
	fmt.Println("authenticated online Drivers:", n)
	if scenario == "stale-gps" {
		a := actors[1]
		var before, after time.Time
		if e = p.QueryRow(ctx, `SELECT recorded_at FROM driver_live_locations WHERE driver_id=$1::uuid`, a.id).Scan(&before); e != nil {
			return e
		}
		if e = a.send("driver.cmd.location", object{"recordedAt": time.Now().Add(-5 * time.Minute), "location": object{"latitude": 6.55, "longitude": 3.5}}); e != nil {
			return e
		}
		time.Sleep(time.Second)
		if e = p.QueryRow(ctx, `SELECT recorded_at FROM driver_live_locations WHERE driver_id=$1::uuid`, a.id).Scan(&after); e != nil {
			return e
		}
		if !before.Equal(after) {
			return errors.New("stale GPS changed live location")
		}
		fmt.Println("stale GPS rejected: passed")
	}
	if scenario == "no-driver" {
		for _, a := range actors[1:] {
			if _, e = request(a, "POST", "/driver/availability", object{"available": false}, ""); e != nil {
				return e
			}
		}
	}

	// Keep idle sockets/GPS live while the selected Driver follows road geometry.
	go func() {
		tick := time.NewTicker(8 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				for _, a := range actors[1:] {
					a.mu.Lock()
					a.socket.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
					a.mu.Unlock()
				}
			}
		}
	}()
	resolved, e := request(rider, "POST", "/rider/agent/resolve", object{"schemaVersion": 1, "guided": true, "message": "Lekki Conservation Centre Lagos", "bias": object{"latitude": 6.428, "longitude": 3.421}}, "")
	if e != nil {
		return e
	}
	place, _ := resolved["place"].(map[string]any)
	if place == nil {
		c, _ := resolved["candidates"].([]any)
		if len(c) == 0 {
			return errors.New("no provider destination candidates")
		}
		v := c[0].(map[string]any)
		resolved, e = request(rider, "POST", "/rider/agent/resolve", object{"schemaVersion": 1, "guided": true, "placeId": v["id"]}, "")
		if e != nil {
			return e
		}
		place, _ = resolved["place"].(map[string]any)
	}
	if place == nil {
		return errors.New("destination unresolved")
	}
	loc := place["location"]
	preview, e := request(rider, "POST", "/trip/preview", object{"schemaVersion": 1, "pickup": object{"latitude": 6.428, "longitude": 3.421}, "destination": loc, "destinationPlaceId": place["id"]}, "")
	if e != nil {
		return e
	}
	fares, _ := preview["rideFares"].([]any)
	if len(fares) == 0 {
		return errors.New("no approved test fare")
	}
	fare := fares[0].(map[string]any)
	var receivedPositions atomic.Int64
	snapshot, e := request(rider, "GET", "/rider/trips/active", nil, "")
	if e != nil {
		return e
	}
	cursor, _ := snapshot["afterEventId"].(string)
	if cursor == "" {
		return errors.New("missing Rider event baseline")
	}
	rh := http.Header{}
	rh.Set("Authorization", "Bearer "+rider.token)
	rh.Set("Origin", "http://localhost:4001")
	rw, _, e := dial("/ws/riders?afterEventId="+url.QueryEscape(cursor), rh)
	if e != nil {
		return errors.New("Rider event socket failed")
	}
	rider.socket = rw
	go func() {
		for {
			var event object
			if rw.ReadJSON(&event) != nil {
				return
			}
			if event["type"] == "driver.event.location_updated" {
				receivedPositions.Add(1)
			}
		}
	}()
	key := uuid.NewString()
	_, e = request(rider, "POST", "/trip/start", object{"rideFareID": fare["id"], "confirmed": true}, key)
	if e != nil {
		return e
	}
	// Exact duplicate is the same request; authoritative fare lookup must return one trip.
	_, e = request(rider, "POST", "/trip/start", object{"rideFareID": fare["id"], "confirmed": true}, key)
	if e != nil {
		return e
	}
	if scenario == "no-driver" {
		lookup, e := request(rider, "GET", "/rider/fares/"+fare["id"].(string)+"/trip", nil, "")
		if e != nil {
			return e
		}
		trip, _ := lookup["trip"].(map[string]any)
		if trip == nil {
			trip = lookup
		}
		tid, _ := trip["id"].(string)
		if tid == "" {
			return errors.New("no-driver trip lookup failed")
		}
		emptyUntil := time.NewTimer(5 * time.Second)
	emptyCheck:
		for {
			select {
			case offered := <-offers:
				if offered.trip == tid {
					return errors.New("offline Driver received current-trip offer")
				}
			case <-emptyUntil.C:
				break emptyCheck
			}
		}
		emptyUntil.Stop()

		outcome, e := waitTrip(rider, tid, "cancelled")
		if e != nil {
			return e
		}
		if outcome["matchingOutcome"] != "no_drivers" {
			return errors.New("missing authoritative no-driver outcome")
		}
		fmt.Println("PASS: zero current-trip offers; backend ended matching with no_drivers")
		return nil
	}
	lookup, e := request(rider, "GET", "/rider/fares/"+fare["id"].(string)+"/trip", nil, "")
	if e != nil {
		return e
	}
	booked, _ := lookup["trip"].(map[string]any)
	tripID, _ := booked["id"].(string)
	if tripID == "" {
		return errors.New("booking returned no trip id")
	}
	var chosen offer
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for chosen.trip == "" {
		select {
		case offered := <-offers:
			if offered.trip == tripID {
				chosen = offered
			}
		case <-deadline.C:
			return errors.New("no current-trip offer received")
		}
	}
	fmt.Println("real socket offer received")
	if scenario == "decline" {
		if e = chosen.a.send("driver.cmd.trip_decline", object{"tripID": chosen.trip}); e != nil {
			return e
		}
		first := chosen.a.id
		select {
		case offered := <-offers:
			if offered.trip != tripID {
				return errors.New("unexpected stale replacement offer; current trip preserved")
			}
			chosen = offered
		case <-time.After(50 * time.Second):
			return errors.New("no replacement offer after decline")
		}
		if chosen.a.id == first {
			return errors.New("declined Driver selected again")
		}
		fmt.Println("decline rematched a different authenticated Driver")
	}

	if e = chosen.a.send("driver.cmd.trip_accept", object{"tripID": chosen.trip}); e != nil {
		return e
	}
	if _, e = waitTrip(rider, chosen.trip, "accepted"); e != nil {
		return e
	}
	if scenario == "reconnect" {
		chosen.a.socket.Close()
		h := http.Header{}
		h.Set("Authorization", "Bearer "+chosen.a.token)
		h.Set("Origin", "http://localhost:4001")
		ws, _, err := dial("/ws/drivers", h)
		if err != nil {
			return errors.New("Driver reconnect failed")
		}
		chosen.a.mu.Lock()
		chosen.a.socket = ws
		chosen.a.mu.Unlock()
		go func() {
			for {
				var v object
				if ws.ReadJSON(&v) != nil {
					return
				}
			}
		}()
		if _, e = waitTrip(rider, chosen.trip, "accepted"); e != nil {
			return e
		}
		fmt.Println("authenticated Driver socket reconnected")
	}
	if scenario == "cancel" {
		_, e = request(rider, "POST", "/trips/"+chosen.trip+"/cancel", object{"reason": "Synthetic cancellation scenario", "confirmed": true}, uuid.NewString())
		if e != nil {
			return e
		}
		_, e = waitTrip(rider, chosen.trip, "cancelled")
		return e
	}
	chosen.a.location(6.428, 3.421)
	for _, stage := range []struct{ path, status string }{{"arrival", "arrived"}, {"start", "started"}} {
		_, e = request(chosen.a, "POST", "/trips/"+chosen.trip+"/"+stage.path, object{}, uuid.NewString())
		if e != nil {
			return e
		}
		if _, e = waitTrip(rider, chosen.trip, stage.status); e != nil {
			return e
		}
	}
	route, _ := preview["route"].(map[string]any)
	routes, _ := route["routes"].([]any)
	if len(routes) > 0 {
		route, _ = routes[0].(map[string]any)
	}
	geometry, _ := route["geometry"].(map[string]any)
	points, _ := geometry["coordinates"].([]any)
	if len(points) < 2 {
		return errors.New("provider returned no road geometry")
	}
	// Accelerated movement samples actual provider geometry; not a travel-time benchmark.
	step := len(points) / 30
	if step < 1 {
		step = 1
	}
	for k := 0; k < len(points); k += step {
		xy := points[k].([]any)
		nextIndex := k + step
		if nextIndex >= len(points) {
			nextIndex = len(points) - 1
		}
		next := points[nextIndex].([]any)
		heading := bearing(xy[1].(float64), xy[0].(float64), next[1].(float64), next[0].(float64))
		if e = chosen.a.locationHeading(xy[1].(float64), xy[0].(float64), heading); e != nil {
			return e
		}
		time.Sleep(time.Second)
	}
	xy := points[len(points)-1].([]any)
	chosen.a.location(xy[1].(float64), xy[0].(float64))
	fmt.Println("real road movement points:", len(points))
	_, e = request(chosen.a, "POST", "/trips/"+chosen.trip+"/complete", object{}, uuid.NewString())
	if e != nil {
		return e
	}
	if _, e = waitTrip(rider, chosen.trip, "completed"); e != nil {
		return e
	}
	var count int
	e = p.QueryRow(ctx, `SELECT count(*) FROM trips WHERE ride_fare_id=$1::uuid`, fare["id"]).Scan(&count)
	if e != nil {
		return e
	}
	if count != 1 {
		return errors.New("duplicate booking invariant failed")
	}
	until := time.Now().Add(10 * time.Second)
	for receivedPositions.Load() < 2 && time.Now().Before(until) {
		time.Sleep(300 * time.Millisecond)
	}
	if receivedPositions.Load() < 2 {
		return errors.New("Rider socket did not receive movement")
	}
	fmt.Println("Rider socket received Driver locations:", receivedPositions.Load())
	fmt.Println("PASS: one booking, real offer/acceptance/arrival/start/movement/completion; settlement not fabricated")
	return nil
}

func verifyLatest() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer p.Close()
	var id, label string
	e = p.QueryRow(ctx, `SELECT t.id::text,u.casper_id_user_id FROM trips t JOIN users u ON u.id=t.rider_id WHERE u.casper_id_user_id LIKE 'heygo-simulation:v1:rider%' AND t.status='completed' ORDER BY t.updated_at DESC LIMIT 1`).Scan(&id, &label)
	if e != nil {
		return errors.New("no completed synthetic trip")
	}
	a, e := provision(ctx, p, strings.TrimPrefix(label, prefix))
	if e != nil {
		return e
	}
	defer p.Exec(context.Background(), `UPDATE user_sessions SET revoked_at=NOW() WHERE id=$1::uuid`, a.session)
	receipt, e := request(a, "GET", "/rider/trips/"+id+"/receipt", nil, "")
	if e != nil {
		return e
	}
	if receipt["reference"] != "trip:"+id || receipt["currency"] != "NGN" || receipt["providerVerified"] != false {
		return errors.New("receipt identity/currency/provider boundary mismatch")
	}
	settlement, _ := receipt["settlement"].(map[string]any)
	history, e := request(a, "GET", "/rider/trips", nil, "")
	if e != nil {
		return e
	}
	items, _ := history["items"].([]any)
	found := false
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		trip, _ := item["trip"].(map[string]any)
		if trip["id"] == id {
			found = true
		}
	}
	if !found {
		return errors.New("completed trip missing from owned history")
	}
	fmt.Println("PASS: completed trip in authenticated history, matching NGN receipt, providerVerified=false, actual settlement:", settlement["status"])
	return nil
}
