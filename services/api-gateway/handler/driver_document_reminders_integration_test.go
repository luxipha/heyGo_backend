package handler

import (
	"context"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"os"
	"testing"
	"time"
)

func TestDriverDocumentRemindersMilestonesAndRenewal(t *testing.T) {
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
	owner, doc, device := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['driver'])`, owner)
	defer pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, owner)
	exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, owner)
	exec(`INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::UUID,$2::UUID,'android',$1)`, device, owner)
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at) VALUES($1::UUID,$2::UUID,'driver_license','approved','private', $3)`, doc, owner, now.Add(20*24*time.Hour))
	run := func(at time.Time) {
		t.Helper()
		if e := runDriverDocumentRemindersAt(ctx, pool, 100, at); e != nil {
			t.Fatal(e)
		}
	}
	count := func(want int) {
		t.Helper()
		var got int
		if e := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID AND type='document_expiry'`, owner).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("reminders=%d want=%d", got, want)
		}
	}
	run(now)
	run(now)
	count(1)
	job, e := claimPushDeliveryForDriver(ctx, pool, owner)
	if e != nil || job == nil {
		t.Fatalf("claim=%v %v", job, e)
	}
	run(now.Add(14 * 24 * time.Hour))
	run(now.Add(19 * 24 * time.Hour))
	run(now.Add(21 * 24 * time.Hour))
	count(4)
	run(now.Add(21 * 24 * time.Hour))
	count(4)
	var stages int
	if e := pool.QueryRow(ctx, `SELECT COUNT(DISTINCT data->>'stage') FROM driver_notifications WHERE driver_id=$1::UUID`, owner).Scan(&stages); e != nil || stages != 4 {
		t.Fatalf("stages=%d err=%v", stages, e)
	}
	// A renewal under review supersedes old evidence and cancels its queued reminders.
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,submitted_at) VALUES($1::UUID,$2::UUID,'driver_license','under_review','renewal',NOW()+INTERVAL '1 second')`, uuid.NewString(), owner)
	if driverPushStillAllowed(ctx, pool, *job) {
		t.Fatal("superseded reminder may not send")
	}
	run(now.Add(40 * 24 * time.Hour))
	count(4)
	// Reminders remain in the inbox with push disabled.
	exec(`INSERT INTO driver_notification_preferences(driver_id,document_reminders) VALUES($1::UUID,FALSE)`, owner)
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at) VALUES($1::UUID,$2::UUID,'vehicle_license','approved','private',$3)`, uuid.NewString(), owner, now.Add(5*24*time.Hour))
	run(now)
	count(5)
	var queued int
	if e := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.driver_id=$1::UUID AND n.data->>'documentType'='vehicle_license'`, owner).Scan(&queued); e != nil || queued != 0 {
		t.Fatalf("disabled push count=%d err=%v", queued, e)
	}
	// Null expiry and rejected evidence never create reminders.
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key) VALUES($1::UUID,$2::UUID,'auto_insurance','approved','private')`, uuid.NewString(), owner)
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at) VALUES($1::UUID,$2::UUID,'roadworthiness','rejected','private',$3)`, uuid.NewString(), owner, now)
	run(now)
	count(5)
}
