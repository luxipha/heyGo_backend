package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

// History, receipt and trip detail must show where the trip went and which
// statutory charges were assessed, and only to the Driver who drove it.
func TestDriverTripPlacesAndStatutoryCharges(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	driver, other, rider, fare, trip, plainFare, plainTrip, rule, admin1, admin2 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{driver, "driver"}, {other, "driver"}, {rider, "rider"}} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, item.id, "places:"+item.id, "places-human:"+item.id, item.role)
	}
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Places Driver','T1','sedan','offline'),($2::UUID,'Other Driver','T2','sedan','offline')`, driver, other)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup_metadata,destination_metadata) VALUES
		($1::UUID,$3::UUID,'sedan',243195,'{"routes":[]}'::JSONB,'{"name":"Current location","address":"","provider":"device"}'::JSONB,'{"id":"p1","name":"Obafemi Awolowo Wy","address":"Obafemi Awolowo Wy, Alausa, Lagos","location":{"latitude":6.61,"longitude":3.35},"provider":"google"}'::JSONB),
		($2::UUID,$3::UUID,'sedan',100000,'{"routes":[]}'::JSONB,NULL,NULL)`, fare, plainFare, rider)
	for _, item := range []struct{ trip, fare string }{{trip, fare}, {plainTrip, plainFare}} {
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,created_at,updated_at,started_at,completed_at)
			VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed',NOW()-INTERVAL '1 hour',NOW()-INTERVAL '5 minutes',NOW()-INTERVAL '30 minutes',NOW()-INTERVAL '5 minutes')`, item.trip, rider, item.fare, driver)
	}
	for i, id := range []string{admin1, admin2} {
		exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, id, "places-"+id[:8]+string(rune('a'+i)))
	}
	exec(`INSERT INTO statutory_charge_rules(id,ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,calculation_config,market_code,bearer,funding_source,status,effective_from,reference,created_by,approved_by,approved_at)
		VALUES($1::UUID,$2,1,'Lagos ride levy','Lagos State Government','NG-LA','fixed','trip_fare','{"amountNaira":5}'::JSONB,'LAGOS','driver','operating_balance','approved',NOW()-INTERVAL '1 day','LASG-2026',$3::UUID,$4::UUID,NOW())`, rule, "PLACES_"+rule[:8], admin1, admin2)
	exec(`INSERT INTO trip_statutory_charges(id,trip_id,rule_id,driver_id,amount_kobo,bearer,funding_source,rule_snapshot)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,500,'driver','operating_balance','{"ledgerCode":"LAGOS_RIDE_LEVY","name":"Lagos ride levy","authority":"Lagos State Government","reference":"LASG-2026"}'::JSONB)`, uuid.NewString(), trip, rule, driver)

	gin.SetMode(gin.TestMode)
	gctx, _ := gin.CreateTestContext(nil)
	gctx.Request = httptest.NewRequest("GET", "/driver/trips", nil)

	history, err := readDriverTripHistory(gctx, pool, driver, 10, nil, nil, nil, "all")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]driverTripHistoryItem{}
	for _, item := range history {
		byID[item.ID] = item
	}
	got := byID[trip]
	if got.Destination == nil || got.Destination.Name != "Obafemi Awolowo Wy" || got.Pickup == nil || got.Pickup.Name != "Current location" {
		t.Fatalf("history is missing places: %+v", got)
	}
	if byID[plainTrip].Pickup != nil || byID[plainTrip].Destination != nil {
		t.Fatalf("a trip without stored places must report none: %+v", byID[plainTrip])
	}

	receipt, err := readDriverTripReceipt(gctx, pool, driver, trip)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Destination == nil || receipt.Destination.Address != "Obafemi Awolowo Wy, Alausa, Lagos" {
		t.Fatalf("receipt is missing the destination: %+v", receipt.Destination)
	}
	if len(receipt.StatutoryCharges) != 1 || receipt.StatutoryCharges[0].AmountKobo != 500 || receipt.StatutoryCharges[0].Name != "Lagos ride levy" || receipt.StatutoryCharges[0].Authority != "Lagos State Government" || receipt.StatutoryCharges[0].Reference != "LASG-2026" {
		t.Fatalf("unexpected charges: %+v", receipt.StatutoryCharges)
	}
	plain, err := readDriverTripReceipt(gctx, pool, driver, plainTrip)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &wire)
	if string(wire["statutoryCharges"]) != "[]" {
		t.Fatalf("a trip without charges must serialise [] not %s", wire["statutoryCharges"])
	}

	// The Rider's name and the average of what Drivers rated them (4 and 5).
	exec(`INSERT INTO rider_profiles(rider_id,display_name) VALUES($1::UUID,'Tunde Qarider')`, rider)
	exec(`INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating) VALUES($1::UUID,$2::UUID,$3::UUID,5),($4::UUID,$2::UUID,$3::UUID,4)`, trip, driver, rider, plainTrip)
	named, err := readDriverTripRider(ctx, pool, driver, trip)
	if err != nil || named == nil {
		t.Fatalf("rider missing: %+v %v", named, err)
	}
	if named.DisplayName != "Tunde Qarider" || named.Rating == nil || *named.Rating != 4.5 || named.RatingCount != 2 {
		t.Fatalf("unexpected rider: %+v", named)
	}
	// Someone else's trip reveals nothing about the Rider.
	if foreign, err := readDriverTripRider(ctx, pool, other, trip); err != nil || foreign != nil {
		t.Fatalf("rider leaked to another driver: %+v %v", foreign, err)
	}

	// Another driver never sees these charges, even by asking for the trip id.
	leaked, err := readDriverStatutoryCharges(ctx, pool, other, trip)
	if err != nil || len(leaked) != 0 {
		t.Fatalf("charges leaked across drivers: %+v %v", leaked, err)
	}

	// The trip detail endpoint carries the same list.
	router := gin.New()
	auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: driver})
	registerDriverRoutes(router.Group("/", auth.Authenticate), pool, nil)
	req := httptest.NewRequest("GET", "/driver/trips/"+trip, nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("detail status %d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Data struct {
			StatutoryCharges []driverStatutoryCharge `json:"statutoryCharges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.Data.StatutoryCharges) != 1 {
		t.Fatalf("trip detail is missing charges: %s", w.Body.String())
	}
	var withRider struct {
		Data struct {
			Rider *driverTripRider `json:"rider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &withRider); err != nil || withRider.Data.Rider == nil || withRider.Data.Rider.DisplayName != "Tunde Qarider" {
		t.Fatalf("trip detail is missing the rider: %s", w.Body.String())
	}
}
