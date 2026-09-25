package repo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"github.com/google/uuid"
)

func TestPostgresTripRepositoryPersistsFareAndTrip(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	riderID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	creatorID, approverID, marketID, regionID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = pool.Exec(cleanupCtx, `UPDATE trip_geofences SET effective_until=NOW() WHERE id IN ($1::UUID,$2::UUID) AND effective_until IS NULL`, marketID, regionID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1::UUID`, riderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,kyc_tier) VALUES($1,$2,$3,ARRAY['rider'],TRUE,'basic')`, riderID, "casper-"+riderID, "human-"+riderID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, username string }{{creatorID, "creator-" + creatorID}, {approverID, "approver-" + approverID}} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test-only')`, item.id, item.username); err != nil {
			t.Fatal(err)
		}
	}
	classificationCode := "LAGOS_" + tripID[:8]
	for _, zone := range []struct{ id, kind, code string }{{marketID, "market", classificationCode}, {regionID, "region", classificationCode}} {
		if _, err := pool.Exec(ctx, `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)
			VALUES($1::UUID,$2,$3,$3,1,ST_GeomFromText('MULTIPOLYGON(((3.3 6.4,3.5 6.4,3.5 6.6,3.3 6.6,3.3 6.4)))',4326),
			'approved',NOW()-INTERVAL '1 day',$4::UUID,$5::UUID,NOW())`, zone.id, zone.kind, zone.code, creatorID, approverID); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewPostgresRepository(pool)
	route := &triptypes.OSRMApiResponse{}
	route.Routes = append(route.Routes, struct {
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}{Distance: 1000, Duration: 300})
	fare := &triptypes.RideFareModel{ID: fareID, RiderID: riderID, PackageSlug: "sedan", TotalFareInPaise: 2500, Route: route,
		Pickup: &sharedtypes.Coordinate{Latitude: 6.45, Longitude: 3.39}, Destination: &sharedtypes.Coordinate{Latitude: 6.50, Longitude: 3.40}}
	if err := repository.SaveRideFare(ctx, fare); err != nil {
		t.Fatalf("save fare: %v", err)
	}
	loadedFare, err := repository.GetRideFareByID(ctx, fareID)
	if err != nil || loadedFare.RiderID != riderID || loadedFare.TotalFareInPaise != 2500 ||
		loadedFare.Pickup == nil || loadedFare.Pickup.Latitude != 6.45 || loadedFare.Pickup.Longitude != 3.39 ||
		loadedFare.Destination == nil || loadedFare.Destination.Latitude != 6.50 || loadedFare.Destination.Longitude != 3.40 {
		t.Fatalf("loaded fare=%#v err=%v", loadedFare, err)
	}
	created, err := repository.Create(ctx, &triptypes.TripModel{ID: tripID, RiderID: riderID, Status: "pending", RideFare: fare})
	if err != nil {
		t.Fatalf("create trip: %v", err)
	}
	loadedTrip, err := repository.GetByID(ctx, created.ID)
	if err != nil || loadedTrip.ID != tripID || loadedTrip.Status != "pending" || loadedTrip.RideFare.ID != fareID {
		t.Fatalf("loaded trip=%#v err=%v", loadedTrip, err)
	}
	var outboxTopic, outboxRecipient string
	if err := pool.QueryRow(ctx, `SELECT topic,recipient_id::TEXT FROM trip_event_outbox WHERE trip_id=$1::UUID`, tripID).Scan(&outboxTopic, &outboxRecipient); err != nil {
		t.Fatalf("created trip must have a durable event: %v", err)
	}
	if outboxTopic != "trip.event.created" || outboxRecipient != riderID {
		t.Fatalf("unexpected outbox event %s for %s", outboxTopic, outboxRecipient)
	}
	var market, origin, destination string
	var tags []string
	if err := pool.QueryRow(ctx, `SELECT market_code,origin_region_code,destination_region_code,regulatory_tags FROM trips WHERE id=$1::UUID`, tripID).Scan(&market, &origin, &destination, &tags); err != nil {
		t.Fatal(err)
	}
	if market != classificationCode || origin != classificationCode || destination != classificationCode || len(tags) != 1 || tags[0] != "LOCAL" {
		t.Fatalf("classification market=%s origin=%s destination=%s tags=%v", market, origin, destination, tags)
	}
	outside := *fare
	outside.Pickup = &sharedtypes.Coordinate{Latitude: 7.0, Longitude: 4.0}
	_, err = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: &outside})
	if !errors.Is(err, ErrTripClassificationUnavailable) {
		t.Fatalf("expected fail-closed classification, got %v", err)
	}
}
