package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	tripevents "github.com/luxipha/heyGo_backend/services/trip-service/events"
	triprepo "github.com/luxipha/heyGo_backend/services/trip-service/repo"
	tripservice "github.com/luxipha/heyGo_backend/services/trip-service/service"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRiderHistoryReceiptRatingAndCommandRecovery(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, other, driver := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{rider, other, driver} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['rider','driver'])`, id)
	}
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Safe driver','TEST','sedan','offline')`, driver)
	defer func() {
		pool.Exec(ctx, `DELETE FROM trip_ratings WHERE actor_id=$1::UUID OR subject_id=$1::UUID`, rider)
		pool.Exec(ctx, `DELETE FROM trips WHERE rider_id=$1::UUID`, rider)
		pool.Exec(ctx, `DELETE FROM ride_fares WHERE rider_id=$1::UUID`, rider)
	}()
	makeTrip := func(state string) string {
		fare, trip := uuid.NewString(), uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,pickup_metadata,destination_metadata) VALUES($1::UUID,$2::UUID,'sedan',120000,'{}',ST_SetSRID(ST_MakePoint(3.39,6.45),4326)::geography,ST_SetSRID(ST_MakePoint(3.4,6.5),4326)::geography,'{"name":"Pickup"}','{"name":"Destination"}')`, fare, rider)
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,completed_at) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,CASE WHEN $5='completed' THEN NOW() ELSE NULL END)`, trip, rider, fare, driver, state)
		return trip
	}
	completed := makeTrip("completed")
	pending := makeTrip("pending")
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,fare_amount_kobo,no_show_debt_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,120000,120000,0,'pending')`, completed, driver, rider)
	read := &riderTripAPI{pool: pool}
	exec(`INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,feedback_tags) VALUES($1::UUID,$2::UUID,$3::UUID,5,ARRAY['easy_pickup'])`, completed, driver, rider)
	profileCall := func(owner, path string, h gin.HandlerFunc) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", path, nil)
		c.Set("authenticated-user", gatewayauth.User{ID: owner, Roles: []string{"rider"}})
		h(c)
		return w
	}
	if w := profileCall(rider, "/rider/profile/metrics", read.profileMetrics); w.Code != 200 || !strings.Contains(w.Body.String(), `"completedTrips":1`) || !strings.Contains(w.Body.String(), `"receivedFeedbackCount":1`) {
		t.Fatalf("profile metrics: %s", w.Body.String())
	}
	if w := profileCall(other, "/rider/profile/feedback", read.profileFeedback); w.Code != 200 || strings.Contains(w.Body.String(), completed) {
		t.Fatalf("foreign feedback: %s", w.Body.String())
	}
	if w := profileCall(rider, "/rider/profile/feedback?limit=1", read.profileFeedback); w.Code != 200 || !strings.Contains(w.Body.String(), completed) || strings.Contains(w.Body.String(), driver) {
		t.Fatalf("owned safe feedback: %s", w.Body.String())
	}
	if w := profileCall(rider, "/rider/profile/feedback?cursor=bad", read.profileFeedback); w.Code != 400 {
		t.Fatal("malformed feedback cursor accepted")
	}

	exec(`UPDATE users SET consumer_trust_score=610,consumer_trust_status='limited',trust_score_updated_at=NOW(),trust_score_policy_version='v2' WHERE id=$1::UUID`, rider)
	exec(`UPDATE users SET consumer_trust_score=consumer_trust_score WHERE id=$1::UUID`, rider)
	if w := profileCall(rider, "/rider/profile/trust-history", read.profileTrustHistory); w.Code != 200 || !strings.Contains(w.Body.String(), `"score":610`) || !strings.Contains(w.Body.String(), `"completeCalculationHistory":false`) {
		t.Fatalf("trust history: %s", w.Body.String())
	}
	if w := profileCall(other, "/rider/profile/trust-history", read.profileTrustHistory); w.Code != 200 || strings.Contains(w.Body.String(), `"score":610`) {
		t.Fatalf("foreign trust history: %s", w.Body.String())
	}
	var snapshotCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM consumer_trust_snapshots WHERE user_id=$1::UUID`, rider).Scan(&snapshotCount); err != nil || snapshotCount != 1 {
		t.Fatalf("unchanged trust duplicated: %d %v", snapshotCount, err)
	}
	exec(`UPDATE users SET consumer_trust_score=NULL WHERE id=$1::UUID`, rider)
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM consumer_trust_snapshots WHERE user_id=$1::UUID`, rider).Scan(&snapshotCount); err != nil || snapshotCount != 0 {
		t.Fatalf("revoked trust retained: %d %v", snapshotCount, err)
	}

	commands := &riderCommandAPI{pool: pool}
	call := func(user, trip, commandID, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Idempotency-Key", commandID)
		c.Params = gin.Params{{Key: "tripID", Value: trip}, {Key: "commandID", Value: commandID}}
		c.Set("authenticated-user", gatewayauth.User{ID: user, Roles: []string{"rider"}})
		handler(c)
		return w
	}
	if w := call(other, completed, "", "", read.receipt); w.Code != 404 {
		t.Fatalf("foreign receipt: %s", w.Body.String())
	}
	for _, state := range []string{"pending", "disputed", "confirmed"} {
		exec(`UPDATE trip_settlements SET status=$2,resolved_at=CASE WHEN $2='pending' THEN NULL ELSE NOW() END WHERE trip_id=$1::UUID`, completed, state)
		w := call(rider, completed, "", "", read.receipt)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"reference":"trip:`+completed+`"`) || !strings.Contains(w.Body.String(), `"providerVerified":false`) || !strings.Contains(w.Body.String(), `"status":"`+state+`"`) {
			t.Fatalf("receipt=%s", w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/rider/trips?limit=1", nil)
	c.Set("authenticated-user", gatewayauth.User{ID: rider, Roles: []string{"rider"}})
	read.history(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), completed) || strings.Contains(w.Body.String(), pending) {
		t.Fatalf("history: %s", w.Body.String())
	}
	issueID := uuid.NewString()
	issueBody := `{"clientMessageId":"` + issueID + `","topicCode":"location","description":"Please check the pickup"}`
	support := &supportAPI{pool: pool}
	firstIssue := call(rider, completed, issueID, issueBody, support.createTripIssue)
	secondIssue := call(rider, completed, issueID, issueBody, support.createTripIssue)
	if firstIssue.Code != 201 || secondIssue.Code != 201 || firstIssue.Body.String() != secondIssue.Body.String() {
		t.Fatalf("issue identity: %s / %s", firstIssue.Body.String(), secondIssue.Body.String())
	}
	var linkedCases int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_cases WHERE opened_by=$1::UUID AND trip_id=$2::UUID`, rider, completed).Scan(&linkedCases); err != nil || linkedCases != 1 {
		t.Fatalf("linked Support cases=%d err=%v", linkedCases, err)
	}
	linked := call(rider, completed, "", "", read.supportCases)
	if linked.Code != 200 || !strings.Contains(linked.Body.String(), `"tripId":"`+completed+`"`) {
		t.Fatalf("linked read=%s", linked.Body.String())
	}
	if foreign := call(other, completed, "", "", read.supportCases); foreign.Code != 404 {
		t.Fatal("foreign linked cases were disclosed")
	}
	consumer := tripevents.NewDriverConsumer(nil, tripservice.NewService(triprepo.NewPostgresRepository(pool)), pool)
	process := func(id, topic string) {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT payload FROM rider_trip_commands WHERE id=$1::UUID`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(contracts.EventMessage{EntityID: rider, Data: raw})
		if err := consumer.Handle(ctx, &pubsub.Message{Topic: topic, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	cancelID := uuid.NewString()
	body := `{"clientCommandId":"` + cancelID + `","confirmed":true,"reason":"Plans changed"}`
	w = call(rider, pending, cancelID, body, func(c *gin.Context) { commands.queue(c, contracts.TripCmdCancel) })
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"outcome":"unresolved"`) {
		t.Fatalf("queued=%s", w.Body.String())
	}
	process(cancelID, contracts.TripCmdCancel)
	process(cancelID, contracts.TripCmdCancel)
	w = call(rider, pending, cancelID, "", commands.read)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"confirmed"`) {
		t.Fatalf("result=%s", w.Body.String())
	}
	var debtCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM rider_no_show_debts WHERE rider_id=$1::UUID`, rider).Scan(&debtCount); err != nil || debtCount != 0 {
		t.Fatalf("automatic debt=%d err=%v", debtCount, err)
	}
	late := makeTrip("pending")
	lateID := uuid.NewString()
	lateBody := `{"clientCommandId":"` + lateID + `","confirmed":true,"reason":"Plans changed"}`
	call(rider, late, lateID, lateBody, func(c *gin.Context) { commands.queue(c, contracts.TripCmdCancel) })
	exec(`UPDATE trips SET status='started' WHERE id=$1::UUID`, late)
	process(lateID, contracts.TripCmdCancel)
	w = call(rider, late, lateID, "", commands.read)
	if !strings.Contains(w.Body.String(), `"status":"rejected"`) {
		t.Fatalf("late cancellation=%s", w.Body.String())
	}
	ratingID := uuid.NewString()
	ratingBody := `{"clientCommandId":"` + ratingID + `","confirmed":true,"rating":1,"comment":"Please review"}`
	w = call(rider, completed, ratingID, ratingBody, func(c *gin.Context) { commands.queue(c, contracts.TripCmdRate) })
	if w.Code != 202 {
		t.Fatalf("rating queue=%s", w.Body.String())
	}
	process(ratingID, contracts.TripCmdRate)
	w = call(rider, completed, "", "", read.rating)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reviewStatus":"pending"`) || !strings.Contains(w.Body.String(), `"eligible":false`) {
		t.Fatalf("submitted rating=%s", w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID AND type='trip.event.command_result' AND data->>'commandId'=$2`, rider, cancelID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate command result=%d err=%v", count, err)
	}
}
