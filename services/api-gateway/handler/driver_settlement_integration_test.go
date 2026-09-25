package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestDriverSettlementManualConfirmAndDisputeAreIdempotent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	riderID, driverID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	disputeFareID, disputeTripID := uuid.NewString(), uuid.NewString()
	// Keep immutable settlement action history intact. Fixtures use fresh UUIDs
	// on every run and this test is intended for a disposable integration DB.
	for _, u := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "settlement-api:"+u.id, "settlement-api:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Test Driver','API-1','sedan','on_trip')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{"routes":[]}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,500000,'pending')`, tripID, driverID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlement_actions(trip_id,actor_id,action,idempotency_key) VALUES($1::UUID,$2::UUID,'pending',$3)`, tripID, driverID, "trip:"+tripID+":settlement:pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',125000,'{"routes":[]}'::JSONB)`, disputeFareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed')`, disputeTripID, riderID, disputeFareID, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,125000,'pending')`, disputeTripID, driverID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trip_settlement_actions(trip_id,actor_id,action,idempotency_key) VALUES($1::UUID,$2::UUID,'pending',$3)`, disputeTripID, driverID, "trip:"+disputeTripID+":settlement:pending"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	users := operatingHistoryTestUsers{id: driverID}
	router.Use(gatewayauth.NewMiddleware(contractTestVerifier{}, users).Authenticate)
	api := &driverAPI{pool: pool}
	router.GET("/driver/trips/:tripID/settlement", api.tripSettlement)
	router.POST("/driver/trips/:tripID/settlement/confirm", api.confirmTripSettlement)
	router.POST("/driver/trips/:tripID/settlement/disputes", api.disputeTripSettlement)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	read := request(http.MethodGet, "/driver/trips/"+tripID+"/settlement", "")
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	confirm := request(http.MethodPost, "/driver/trips/"+tripID+"/settlement/confirm", "")
	confirmAgain := request(http.MethodPost, "/driver/trips/"+tripID+"/settlement/confirm", "")
	if confirm.Code != http.StatusOK || confirmAgain.Code != http.StatusOK {
		t.Fatalf("confirm statuses=%d,%d bodies=%s %s", confirm.Code, confirmAgain.Code, confirm.Body.String(), confirmAgain.Body.String())
	}
	var response struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(confirm.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Status != "confirmed" {
		t.Fatalf("manual confirmation status=%q", response.Data.Status)
	}
	conflict := request(http.MethodPost, "/driver/trips/"+tripID+"/settlement/disputes", `{"note":"not received"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting dispute status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	disputePath := "/driver/trips/" + disputeTripID + "/settlement/disputes"
	dispute := request(http.MethodPost, disputePath, `{"note":"rider payment not received"}`)
	disputeAgain := request(http.MethodPost, disputePath, `{"note":"rider payment not received"}`)
	if dispute.Code != http.StatusOK || disputeAgain.Code != http.StatusOK {
		t.Fatalf("dispute statuses=%d,%d", dispute.Code, disputeAgain.Code)
	}
	disputeRead := request(http.MethodGet, "/driver/trips/"+disputeTripID+"/settlement", "")
	var disputeResponse struct {
		Data struct {
			Status     string `json:"status"`
			DriverNote string `json:"driverNote"`
		} `json:"data"`
	}
	if err := json.Unmarshal(disputeRead.Body.Bytes(), &disputeResponse); err != nil {
		t.Fatal(err)
	}
	if disputeResponse.Data.Status != "disputed" || disputeResponse.Data.DriverNote != "rider payment not received" {
		t.Fatalf("dispute record=%+v", disputeResponse.Data)
	}
	var actions, outboxRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_settlement_actions WHERE trip_id=$1::UUID AND action='confirmed'`, tripID).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_event_outbox WHERE trip_id=$1::UUID AND topic='trip.event.settlement_updated' AND attempt=1`, tripID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if actions != 1 || outboxRows != 2 {
		t.Fatalf("confirmation actions=%d event recipients=%d; want 1 and 2", actions, outboxRows)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_settlement_actions WHERE trip_id=$1::UUID AND action='disputed'`, disputeTripID).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_event_outbox WHERE trip_id=$1::UUID AND topic='trip.event.settlement_updated' AND attempt=2`, disputeTripID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if actions != 1 || outboxRows != 2 {
		t.Fatalf("dispute actions=%d event recipients=%d; want 1 and 2", actions, outboxRows)
	}
	if _, err := pool.Exec(ctx, `UPDATE trip_event_outbox SET published_at=NOW() WHERE trip_id IN ($1::UUID,$2::UUID) AND published_at IS NULL`, tripID, disputeTripID); err != nil {
		t.Fatalf("mark test outbox rows drained: %v", err)
	}
}
