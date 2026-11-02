package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSimulationRecoveryAndClonePreserveOwnedBoundaries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 5})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	staff, run, participant, owned, other, session, otherSession := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO admin_users(id,username,password_hash,active)VALUES($1::uuid,$1,'!test-login-disabled',FALSE)`, staff)
	for _, id := range []string{owned, other} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,active)VALUES($1::uuid,$1,$1,ARRAY['rider'],TRUE)`, id)
	}
	for i, id := range []string{owned, other} {
		sid := session
		if i == 1 {
			sid = otherSession
		}
		exec(`INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name)VALUES($1::uuid,$2::uuid,$1,$1,NOW()+INTERVAL '10 minutes',NOW()+INTERVAL '15 minutes','Simulation runtime')`, sid, id)
	}
	exec(`INSERT INTO simulation_runs(id,name,seed,created_by,status)VALUES($1::uuid,'Recovery verification',42,$2::uuid,'running')`, run, staff)
	exec(`INSERT INTO simulation_participants(id,simulation_run_id,role,name,configuration,actor_user_id)VALUES($1::uuid,$2::uuid,'rider','Owned Rider','{"role":"rider","name":"Owned Rider","controller":"simulator","latitude":6.428,"longitude":3.421}'::jsonb,$3::uuid)`, participant, run, owned)
	api := &adminAPI{pool: pool}
	call := func(body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"})
		c.Params = gin.Params{{Key: "runID", Value: run}, {Key: "participantID", Value: participant}}
		handler(c)
		return w
	}
	lease, e := pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lease.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, "simulation:"+run); e != nil {
		t.Fatal(e)
	}
	blocked := call(`{"revision":1}`, api.recoverSimulation)
	if blocked.Code != 409 {
		t.Fatalf("active lease recovery status %d", blocked.Code)
	}
	lease.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "simulation:"+run)
	lease.Release()
	// A Driver-only action cannot be queued for the Rider even with staff authority.
	bad := call(`{"commandId":"`+uuid.NewString()+`","action":"availability","available":true}`, api.queueSimulationRider)
	if bad.Code != 409 {
		t.Fatalf("cross-role action status %d", bad.Code)
	}
	recovered := call(`{"revision":1}`, api.recoverSimulation)
	if recovered.Code != 200 {
		t.Fatalf("recovery: %d %s", recovered.Code, recovered.Body.String())
	}
	var ownRevoked, otherRevoked bool
	if e = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM user_sessions WHERE id=$1::uuid`, session).Scan(&ownRevoked); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM user_sessions WHERE id=$1::uuid`, otherSession).Scan(&otherRevoked); e != nil {
		t.Fatal(e)
	}
	if !ownRevoked || otherRevoked {
		t.Fatal("recovery escaped owned sessions")
	}
	cloned := call(`{"revision":2,"name":"Repeat recovered conditions"}`, api.cloneSimulation)
	if cloned.Code != 201 {
		t.Fatalf("clone: %d %s", cloned.Code, cloned.Body.String())
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(cloned.Body.Bytes(), &response)
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM simulation_participants WHERE simulation_run_id=$1::uuid AND actor_user_id IS NULL AND runtime_status='configured'`, response.Data.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("clone reused runtime identities")
	}
	// Preserve fixtures and history for inspection; no destructive test cleanup.
}
