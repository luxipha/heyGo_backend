package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestSOSIntakeLocationRetention(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	dbctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, driver, fare, trip, request := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{rider, "rider"}, {driver, "driver"}} {
		if _, err := pool.Exec(dbctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, item.id, "sos:"+item.id, "sos-human:"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'SOS Test','SOS-TEST','sedan','available')`, driver); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup) VALUES($1::UUID,$2::UUID,'sedan',5000,'{}'::JSONB,ST_SetSRID(ST_Point(3.4,6.5),4326)::geography)`, fare, rider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'accepted')`, trip, rider, fare, driver); err != nil {
		t.Fatal(err)
	}
	var caseID, incidentID string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		for _, step := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM sos_intakes WHERE trip_id=$1::UUID`, []any{trip}},
			{`DELETE FROM safety_incidents WHERE id=$1::UUID`, []any{incidentID}},
			{`DELETE FROM support_cases WHERE id=$1::UUID`, []any{caseID}},
			{`DELETE FROM trips WHERE id=$1::UUID`, []any{trip}},
			{`DELETE FROM ride_fares WHERE id=$1::UUID`, []any{fare}},
			{`DELETE FROM drivers WHERE id=$1::UUID`, []any{driver}},
			{`DELETE FROM users WHERE id IN ($1::UUID,$2::UUID)`, []any{rider, driver}},
		} {
			if _, err := pool.Exec(cleanup, step.query, step.args...); err != nil {
				t.Errorf("cleanup %s: %v", step.query, err)
			}
		}
	}()
	a := &supportAPI{pool: pool}
	body := `{"clientRequestId":"` + request + `","location":{"latitude":6.5,"longitude":3.4,"capturedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`
	makeCall := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/trips/"+trip+"/sos", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Params = gin.Params{{Key: "tripID", Value: trip}}
		ctx.Set("authenticated-user", gatewayauth.User{ID: rider, Roles: []string{"rider"}})
		a.createSOS(ctx)
		return rec
	}
	first := makeCall()
	if first.Code != http.StatusCreated {
		t.Fatalf("SOS status=%d body=%s", first.Code, first.Body.String())
	}
	if second := makeCall(); second.Code != http.StatusOK {
		t.Fatalf("SOS retry status=%d body=%s", second.Code, second.Body.String())
	}
	body = `{"clientRequestId":"` + request + `","location":{"latitude":6.5,"longitude":3.4,"capturedAt":"` + time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"}}`
	if replay := makeCall(); replay.Code != http.StatusOK {
		t.Fatalf("stale original SOS retry must recover intake: %s", replay.Body.String())
	}
	body = `{"clientRequestId":"` + uuid.NewString() + `","location":{"latitude":6.5,"longitude":3.4,"capturedAt":"` + time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"}}`
	if stale := makeCall(); stale.Code != http.StatusBadRequest {
		t.Fatalf("new stale SOS must be rejected: %s", stale.Body.String())
	}
	if err := pool.QueryRow(dbctx, `SELECT i.case_id::TEXT,i.id::TEXT FROM safety_incidents i JOIN sos_intakes s ON s.incident_id=i.id WHERE s.trip_id=$1::UUID`, trip).Scan(&caseID, &incidentID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(dbctx, `SELECT COUNT(*) FROM sos_intakes WHERE reporter_id=$1::UUID`, rider).Scan(&count); err != nil || count != 1 {
		t.Fatalf("SOS intakes=%d err=%v", count, err)
	}
	if _, err := pool.Exec(dbctx, `UPDATE safety_incidents SET status='resolved',resolved_at=NOW()-INTERVAL '31 days' WHERE id=$1::UUID`, incidentID); err != nil {
		t.Fatal(err)
	}
	if err := RunSOSLocationPurgeOnce(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	var locationRemoved bool
	if err := pool.QueryRow(dbctx, `SELECT location IS NULL AND location_recorded_at IS NULL FROM sos_intakes WHERE incident_id=$1::UUID`, incidentID).Scan(&locationRemoved); err != nil || !locationRemoved {
		t.Fatalf("expired SOS coordinate retained: removed=%t err=%v", locationRemoved, err)
	}
}
