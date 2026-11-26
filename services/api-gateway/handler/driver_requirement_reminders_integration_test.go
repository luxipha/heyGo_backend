package handler

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

// Drivers who joined before an item became required are told once per stage,
// only while they still lack it. Drivers who sign up afterwards are not.
func TestRequirementRemindersOnRealSchema(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	driver := func(ageDays int) string {
		id := uuid.NewString()
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,created_at) VALUES($1::uuid,$1,$1,ARRAY['driver'],now()-make_interval(days=>$2))`, id, ageDays)
		exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::uuid)`, id)
		return id
	}
	notices := func(id string) (n int) {
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::uuid AND title='New requirement'`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	waiting := driver(30)
	covered := driver(30)
	exec(`INSERT INTO driver_vehicles(driver_id,make,model,plate,review_status,approval_status) VALUES($1::uuid,'Toyota','Corolla','QA2','approved','approved')`, covered)
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata) VALUES(gen_random_uuid(),$1::uuid,'hackney_permit','approved','k','{}'::jsonb)`, covered)
	exec(`UPDATE driver_requirement_config SET mode='required',grace_days=14,required_since=now() WHERE type='hackney_permit'`)
	newcomer := driver(0)
	exec(`UPDATE users SET created_at=now()+interval '1 second' WHERE id=$1::uuid`, newcomer)

	if err = RunDriverRequirementRemindersOnce(ctx, pool, 5000); err != nil {
		t.Fatal(err)
	}
	if notices(waiting) != 1 {
		t.Fatalf("waiting driver should be told once, got %d", notices(waiting))
	}
	if notices(covered) != 0 || notices(newcomer) != 0 {
		t.Fatal("a driver who already has it, or who joined after the change, was told")
	}
	if err = RunDriverRequirementRemindersOnce(ctx, pool, 5000); err != nil {
		t.Fatal(err)
	}
	if notices(waiting) != 1 {
		t.Fatal("the same stage was announced twice")
	}
	exec(`UPDATE driver_requirement_config SET mode='off',required_since=NULL WHERE type='hackney_permit'`)
}
