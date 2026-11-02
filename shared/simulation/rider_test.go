package simulation

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestActorCommandsRequireConfirmationAndClosedVocabulary(t *testing.T) {
	id := uuid.NewString()
	trip := uuid.NewString()
	for _, action := range []RiderAction{
		{CommandID: id, Action: "arbitraryEndpoint"},
		{CommandID: id, Action: "book", FareID: trip},
		{CommandID: id, Action: "cancel", TripID: trip, Reason: "simulation"},
		{CommandID: id, Action: "settlementConfirm", TripID: trip},
		{CommandID: id, Action: "noShowClaim", TripID: trip},
		{CommandID: id, Action: "availability"},
		{CommandID: id, Action: "autoAccept"},
		{CommandID: id, Action: "preview", PlaceID: "place", DelaySeconds: 541},
	} {
		if action.Validate() == nil {
			t.Fatalf("unsafe action accepted: %s", action.Action)
		}
	}
	for _, action := range []RiderAction{{CommandID: id, Action: "book", FareID: trip, Confirmed: true}, {CommandID: id, Action: "cancel", TripID: trip, Reason: "simulation", Confirmed: true}, {CommandID: id, Action: "settlementConfirm", TripID: trip, Confirmed: true}} {
		if e := action.Validate(); e != nil {
			t.Fatal(e)
		}
	}
	if (RiderAction{Action: "settlementConfirm"}).OwnerRole() != "driver" || (RiderAction{Action: "book"}).OwnerRole() != "rider" {
		t.Fatal("actor role boundary lost")
	}
}
func TestRiderPreviewResolvesRealPlaceAndNeverStartsTrip(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		paths = append(paths, req.URL.Path)
		if req.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing actor auth")
		}
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if req.URL.Path == "/rider/agent/resolve" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"place": map[string]any{"id": "place", "location": map[string]float64{"latitude": 6.43, "longitude": 3.42}}}})
			return
		}
		if req.URL.Path != "/trip/preview" {
			t.Error("preview executed another endpoint")
		}
		if body["destinationPlaceId"] != "place" {
			t.Error("lost authoritative place")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"rideFares": []any{}}})
	}))
	defer server.Close()
	r := Runtime{BaseURL: server.URL}
	a := &liveActor{Actor: Actor{Token: "fixture", Config: Participant{Latitude: 6.428, Longitude: 3.421}}}
	_, e := r.riderAction(context.Background(), a, RiderAction{CommandID: uuid.NewString(), Action: "preview", PlaceID: "place"})
	if e != nil {
		t.Fatal(e)
	}
	if len(paths) != 2 {
		t.Fatal(paths)
	}
}
func TestConfirmedBookingKeepsStableIdempotencyIdentity(t *testing.T) {
	id := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/trip/start" || req.Header.Get("Idempotency-Key") != id {
			t.Error("booking identity/path changed")
		}
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if body["confirmed"] != true {
			t.Error("confirmation missing")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "accepted"}})
	}))
	defer server.Close()
	r := Runtime{BaseURL: server.URL}
	a := &liveActor{Actor: Actor{Token: "fixture"}}
	b := RiderAction{CommandID: id, Action: "book", FareID: uuid.NewString(), Confirmed: true}
	for i := 0; i < 2; i++ {
		if _, e := r.riderAction(context.Background(), a, b); e != nil {
			t.Fatal(e)
		}
	}
}

func TestQueuedClientsUseBoundedConcurrency(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 6})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	staff, run := uuid.NewString(), uuid.NewString()
	if _, e = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,active)VALUES($1::uuid,$1,'!test-disabled',FALSE)`, staff); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO simulation_runs(id,name,seed,created_by)VALUES($1::uuid,'Concurrent queue check',1,$2::uuid)`, run, staff); e != nil {
		t.Fatal(e)
	}
	actors := map[string]*liveActor{}
	for i := 0; i < 12; i++ {
		id, cmd := uuid.NewString(), uuid.NewString()
		if _, e = pool.Exec(ctx, `INSERT INTO simulation_participants(id,simulation_run_id,role,name,configuration)VALUES($1::uuid,$2::uuid,'rider','Queue Rider','{}')`, id, run); e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(RiderAction{CommandID: cmd, Action: "search", Message: "Lagos"})
		if _, e = pool.Exec(ctx, `INSERT INTO simulation_rider_actions(id,simulation_run_id,participant_id,action,payload)VALUES($1::uuid,$2::uuid,$3::uuid,'search',$4::jsonb)`, cmd, run, id, body); e != nil {
			t.Fatal(e)
		}
		actors[id] = &liveActor{Actor: Actor{ID: uuid.NewString(), Token: "fixture-only", Config: Participant{Role: "rider"}}}
	}
	var active, maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "resolved"}})
	}))
	defer server.Close()
	runtime := Runtime{Pool: pool, BaseURL: server.URL}
	runtime.riderCommands(ctx, run, actors, 3)
	if maxActive.Load() < 2 || maxActive.Load() > 3 {
		t.Fatalf("observed concurrency %d", maxActive.Load())
	}
	var completed int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM simulation_rider_actions WHERE simulation_run_id=$1::uuid AND status='succeeded'`, run).Scan(&completed); e != nil || completed != 12 {
		t.Fatalf("completed=%d err=%v", completed, e)
	}
}

func TestStoppedRunCannotProvisionLateWorker(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	staff, run, participant := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, e = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,active)VALUES($1::uuid,$1,'!test-disabled',FALSE)`, staff); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO simulation_runs(id,name,seed,created_by,status)VALUES($1::uuid,'Stopped worker regression',1,$2::uuid,'stopped')`, run, staff); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO simulation_participants(id,simulation_run_id,role,name,configuration)VALUES($1::uuid,$2::uuid,'rider','Stopped Rider','{"role":"rider","name":"Stopped Rider","controller":"simulator","latitude":6.428,"longitude":3.421}')`, participant, run); e != nil {
		t.Fatal(e)
	}
	runtime := Runtime{Pool: pool, BaseURL: "http://127.0.0.1:1"}
	runtime.Run(run)
	var untouched bool
	if e = pool.QueryRow(ctx, `SELECT p.actor_user_id IS NULL AND r.status='stopped' AND r.revision=1 FROM simulation_participants p JOIN simulation_runs r ON r.id=p.simulation_run_id WHERE p.id=$1::uuid`, participant).Scan(&untouched); e != nil || !untouched {
		t.Fatalf("late worker changed stopped run: %v", e)
	}
}
