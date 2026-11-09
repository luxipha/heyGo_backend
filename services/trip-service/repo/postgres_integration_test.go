package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
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
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM trips WHERE rider_id=$1::UUID`, riderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ride_fares WHERE rider_id=$1::UUID`, riderID)
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
	route.Routes = append(route.Routes, geo.Route{Distance: 1000, Duration: 300})
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

	// Contact readiness is authoritative even for an otherwise valid owned fare.
	riderPhone := fmt.Sprintf("+23480%08d", uint64(uuid.MustParse(riderID).ID())%100000000)
	if _, e := pool.Exec(ctx, `UPDATE users SET phone_e164=$2 WHERE id=$1::uuid`, riderID, riderPhone); e != nil {
		t.Fatal(e)
	}
	for _, change := range []string{
		"email_verified=FALSE,phone_verified_at=NULL",
		"email=$2,email_verified=TRUE,phone_verified_at=NULL",
		"email_verified=FALSE,phone_verified_at=NOW()",
		"email='',email_verified=TRUE,phone_e164='invalid',phone_verified_at=NOW()",
	} {
		args := []any{riderID}
		if strings.Contains(change, "$2") {
			args = append(args, riderID+"@example.test")
		}
		if _, e := pool.Exec(ctx, `UPDATE users SET `+change+` WHERE id=$1::uuid`, args...); e != nil {
			t.Fatal(e)
		}
		if _, e := repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: fare}); !errors.Is(e, ErrRiderContactUnverified) {
			t.Fatalf("contact gate %s: %v", change, e)
		}
	}
	if _, e := pool.Exec(ctx, `UPDATE users SET email=$2,email_verified=TRUE,phone_e164=$3,phone_verified_at=NOW() WHERE id=$1::uuid`, riderID, riderID+"@example.test", riderPhone); e != nil {
		t.Fatal(e)
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
	var outboxPayload []byte
	if err := pool.QueryRow(ctx, `SELECT topic,recipient_id::TEXT,payload FROM trip_event_outbox WHERE trip_id=$1::UUID`, tripID).Scan(&outboxTopic, &outboxRecipient, &outboxPayload); err != nil {
		t.Fatalf("created trip must have a durable event: %v", err)
	}
	if outboxTopic != "trip.event.created" || outboxRecipient != riderID {
		t.Fatalf("unexpected outbox event %s for %s", outboxTopic, outboxRecipient)
	}
	var money struct {
		Trip struct {
			SelectedFare struct {
				FareKobo      int64  `json:"fareKobo"`
				AmountDueKobo int64  `json:"amountDueKobo"`
				Currency      string `json:"currency"`
			} `json:"selectedFare"`
		} `json:"trip"`
	}
	if err := json.Unmarshal(outboxPayload, &money); err != nil {
		t.Fatalf("non-integer public money: %s: %v", outboxPayload, err)
	}
	if money.Trip.SelectedFare.FareKobo != 2500 || money.Trip.SelectedFare.AmountDueKobo != 2500 || money.Trip.SelectedFare.Currency != "NGN" {
		t.Fatalf("public fare missing: %s", outboxPayload)
	}
	var market, origin, destination string
	var tags []string
	if err := pool.QueryRow(ctx, `SELECT market_code,origin_region_code,destination_region_code,regulatory_tags FROM trips WHERE id=$1::UUID`, tripID).Scan(&market, &origin, &destination, &tags); err != nil {
		t.Fatal(err)
	}
	if market != classificationCode || origin != classificationCode || destination != classificationCode || len(tags) != 1 || tags[0] != "LOCAL" {
		t.Fatalf("classification market=%s origin=%s destination=%s tags=%v", market, origin, destination, tags)
	}
	if _, err := pool.Exec(ctx, `UPDATE ride_fares SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::UUID`, fareID); err != nil {
		t.Fatal(err)
	}
	// Recovering an existing trip is not a new booking and remains available.
	if _, e := pool.Exec(ctx, `UPDATE users SET email_verified=FALSE,phone_verified_at=NULL WHERE id=$1::uuid`, riderID); e != nil {
		t.Fatal(e)
	}
	retried, err := repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: fare})
	if err != nil || retried.ID != tripID {
		t.Fatalf("single-use recovery trip=%v err=%v", retried, err)
	}
	if _, e := pool.Exec(ctx, `UPDATE users SET email_verified=TRUE,phone_verified_at=NOW() WHERE id=$1::uuid`, riderID); e != nil {
		t.Fatal(e)
	}
	newFare := func() *triptypes.RideFareModel {
		copy := *fare
		copy.ID = uuid.NewString()
		copy.PreviewID = ""
		copy.ConsumedTripID = ""
		if err := repository.SaveRideFare(ctx, &copy); err != nil {
			t.Fatal(err)
		}
		return &copy
	}
	_, err = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: newFare()})
	if !errors.Is(err, ErrActiveRiderTrip) {
		t.Fatalf("different fare active conflict: %v", err)
	}
	outside := *newFare()
	outside.Pickup = &sharedtypes.Coordinate{Latitude: 7.0, Longitude: 4.0}
	_, err = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: &outside})
	if !errors.Is(err, ErrTripClassificationUnavailable) {
		t.Fatalf("classification: %v", err)
	}
	expired := newFare()
	if _, err := pool.Exec(ctx, `UPDATE ride_fares SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::UUID`, expired.ID); err != nil {
		t.Fatal(err)
	}
	_, err = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: expired})
	if !errors.Is(err, ErrFareExpired) {
		t.Fatalf("expired fare: %v", err)
	}
	if _, changed, err := repository.Cancel(ctx, tripID, riderID, "Changed plans"); err != nil || !changed {
		t.Fatalf("cancel: changed=%v err=%v", changed, err)
	}
	var cancellationActor string
	if err := pool.QueryRow(ctx, `SELECT payload->'trip'->>'cancellationActor' FROM trip_event_outbox WHERE trip_id=$1::uuid AND topic='trip.event.cancelled' AND recipient_id=$2::uuid`, tripID, riderID).Scan(&cancellationActor); err != nil || cancellationActor != "rider" {
		t.Fatalf("cancel event actor=%s err=%v", cancellationActor, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	concurrentFares := []*triptypes.RideFareModel{newFare(), newFare()}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(fare *triptypes.RideFareModel) {
			defer wg.Done()
			_, err := repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: riderID, Status: "pending", RideFare: fare})
			results <- err
		}(concurrentFares[i])
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrActiveRiderTrip) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent requests: %d successes %d conflicts", successes, conflicts)
	}
}
