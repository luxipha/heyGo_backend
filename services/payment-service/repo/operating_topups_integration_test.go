package repo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/google/uuid"
)

func TestOperatingTopupPostsExactlyOnce(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 2, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driverID, topupID := uuid.NewString(), uuid.NewString()
	paymentReference, transactionReference := "heygo-ob-"+topupID, "MNFY|"+topupID
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "test-"+driverID, "test-"+driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::UUID,$2::UUID,'ios',$3)`, uuid.NewString(), driverID, "test-token-"+driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_topups(id,driver_id,amount_kobo,provider,provider_reference,status)
		VALUES($1::UUID,$2::UUID,500000,'monnify',$3,'pending')`, topupID, driverID, paymentReference); err != nil {
		t.Fatal(err)
	}
	if _, err := PostVerifiedOperatingTopup(ctx, pool, paymentReference, transactionReference, 100); !errors.Is(err, ErrTopupMismatch) {
		t.Fatalf("wrong amount accepted: %v", err)
	}
	first, err := PostVerifiedOperatingTopup(ctx, pool, paymentReference, transactionReference, 500000)
	if err != nil || first.AlreadyPosted || first.BalanceAfterKobo != 500000 {
		t.Fatalf("first posting: %#v, %v", first, err)
	}
	second, err := PostVerifiedOperatingTopup(ctx, pool, paymentReference, transactionReference, 500000)
	if err != nil || !second.AlreadyPosted || second.BalanceAfterKobo != 500000 {
		t.Fatalf("duplicate posting: %#v, %v", second, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_operating_entries WHERE source_key=$1`, "monnify:"+transactionReference).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ledger rows=%d, err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID AND type='balance'`, driverID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("top-up notifications=%d, err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.driver_id=$1::UUID AND p.status='pending'`, driverID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending push deliveries=%d, err=%v", count, err)
	}
}
