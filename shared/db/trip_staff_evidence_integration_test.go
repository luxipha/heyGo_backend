package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTripStaffEvidenceCapturesFutureTransitionsAndVehicle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := NewPostgresPool(ctx, Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	riderID, driverID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, person := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, person.id, "evidence:"+person.id, "evidence-human:"+person.id, person.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Evidence Driver','OLD-PLATE','sedan','offline')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_vehicles(driver_id,package_slug,make,model,model_year,color,plate) VALUES($1::UUID,'sedan','Toyota','Corolla',2022,'white','ACCEPTED-PLATE')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',5000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, tripID, riderID, fareID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT before_acceptance_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE trips SET accepted_vehicle_snapshot='{}'::JSONB WHERE id=$1::UUID`, tripID); err == nil {
		t.Fatal("snapshot could be forged before acceptance")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT before_acceptance_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE trips SET assigned_driver_id=$2::UUID,status='accepted',updated_at=NOW() WHERE id=$1::UUID`, tripID, driverID); err != nil {
		t.Fatal(err)
	}
	var transitions int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM trip_status_events WHERE trip_id=$1::UUID`, tripID).Scan(&transitions); err != nil || transitions != 2 {
		t.Fatalf("transitions=%d err=%v", transitions, err)
	}
	var plate string
	if err := tx.QueryRow(ctx, `SELECT accepted_vehicle_snapshot->>'plate' FROM trips WHERE id=$1::UUID`, tripID).Scan(&plate); err != nil || plate != "ACCEPTED-PLATE" {
		t.Fatalf("snapshot plate=%s err=%v", plate, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_vehicles SET plate='NEW-PLATE' WHERE driver_id=$1::UUID`, driverID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT accepted_vehicle_snapshot->>'plate' FROM trips WHERE id=$1::UUID`, tripID).Scan(&plate); err != nil || plate != "ACCEPTED-PLATE" {
		t.Fatalf("snapshot changed: %s err=%v", plate, err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT evidence_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE trips SET accepted_vehicle_snapshot='{}'::JSONB WHERE id=$1::UUID`, tripID); err == nil {
		t.Fatal("accepted vehicle snapshot was mutable")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT evidence_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM trip_status_events WHERE trip_id=$1::UUID`, tripID); err == nil {
		t.Fatal("status events were deletable")
	}
}
