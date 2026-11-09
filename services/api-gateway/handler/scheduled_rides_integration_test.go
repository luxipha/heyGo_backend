package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	driverrepo "github.com/luxipha/heyGo_backend/services/driver-service/repo"
	triprepo "github.com/luxipha/heyGo_backend/services/trip-service/repo"
	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/scheduling"
)

func TestScheduledLocalBookingAndRecovery(t *testing.T) {
	t.Setenv("HEYGO_SCHEDULE_EDITS_ENABLED", "true")
	dsn := os.Getenv("SCHEDULE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable scheduling database required")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	rider, other, driver, second, staff, approver := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{rider, other, driver, second} {
		exec(`INSERT INTO users(id,roles,email_verified)VALUES($1::uuid,ARRAY['rider','driver'],true)`, id)
	}
	for _, id := range []string{staff, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, id)
	}
	market := "SCHEDULE_" + uuid.NewString()
	region := market + "_REGION"
	pricing := uuid.NewString()
	defer pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=NOW() WHERE code IN($1,$2) AND effective_until IS NULL`, market, region)
	for _, v := range []struct{ kind, code string }{{"market", market}, {"region", region}} {
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,$3,$3,1,ST_GeomFromText('MULTIPOLYGON(((3.8 6.8,4 6.8,4 7,3.8 7,3.8 6.8)))',4326),'approved',NOW()-INTERVAL '1 day',$4::uuid,$5::uuid,NOW())`, uuid.NewString(), v.kind, v.code, staff, approver)
	}
	exec(`INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,1,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW())`, pricing, market, staff, approver)
	exec(`INSERT INTO pricing_package_rates VALUES($1::uuid,'sedan',1000,100,100)`, pricing)
	exec(`INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,1,1,1,false,0,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW())`, uuid.NewString(), market, staff, approver)
	for _, id := range []string{driver, second} {
		exec(`UPDATE users SET phone_verified_at=NOW(),phone_e164='+999'||lpad((floor(random()*999999999999)::bigint)::text,12,'0') WHERE id=$1::uuid`, id)
		exec(`INSERT INTO driver_profiles(driver_id,manual_verification_status,admin_status,admin_reviewed_at)VALUES($1::uuid,'approved','approved',NOW()+INTERVAL '1 minute')`, id)
		exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status,available,online_requested,online_market_code,location)VALUES($1::uuid,'QA','QA','sedan','available',true,true,$2,ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography)`, id, market)
		car := uuid.NewString()
		exec(`INSERT INTO driver_vehicle_records(id,driver_id,make,model,plate,package_slug,review_status,approval_status,approved_at)VALUES($1::uuid,$2::uuid,'Toyota','Corolla',$1,'sedan','approved','approved',NOW()+INTERVAL '1 minute')`, car, id)
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, `SELECT set_config('heygo.vehicle_id',$1,true)`, car); e != nil {
			t.Fatal(e)
		}
		for _, kind := range []string{"vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
			if _, e = tx.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key)VALUES(gen_random_uuid(),$1::uuid,$2,'approved','test')`, id, kind); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO driver_inspections(id,driver_id,status,vehicle_revision)VALUES(gen_random_uuid(),$1::uuid,'completed',0)`, id); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key)VALUES(gen_random_uuid(),$1::uuid,'driver_license','approved','test')`, id)
		exec(`INSERT INTO driver_live_locations(driver_id,location)SELECT id,location FROM drivers WHERE id=$1::uuid`, id)
		exec(`INSERT INTO driver_socket_sessions VALUES($1::uuid,$2::uuid,NOW()+INTERVAL '1 day')`, id, uuid.NewString())
		exec(`INSERT INTO driver_operating_accounts(driver_id,balance_kobo)VALUES($1::uuid,100000)`, id)
	}
	exec(`INSERT INTO scheduled_ride_policies(enabled,market_codes,category_slugs)VALUES(true,ARRAY[$1],ARRAY['sedan'])`, market)
	router := gin.New()
	group := router.Group("", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Owner"), Roles: []string{"rider", "driver"}})
	})
	registerScheduledRoutes(group, pool)
	api := &scheduledAPI{pool: pool, router: optionsTestRouter{}}
	group.POST("/test/:bookingID/ready", api.ready)
	driverAPI := &driverAPI{pool: pool}
	group.GET("/test/no-show/:tripID", driverAPI.noShowEligibility)
	group.POST("/test/no-show/:tripID", driverAPI.submitNoShowClaim)
	admin := &adminAPI{pool: pool}
	group.PUT("/test/scheduling/settings", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff}); admin.saveSchedulePolicy(c) })
	call := func(who, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Owner", who)
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	makeFare := func(owner string) string {
		id := uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination,pickup_metadata,destination_metadata,pricing_version_id,pricing_market_code)VALUES($1::uuid,$2::uuid,'sedan',10000,'{"routes":[{"duration":600,"distance":1000,"geometry":{"coordinates":[[3.9,6.9],[3.91,6.91]]}}]}',ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,ST_SetSRID(ST_MakePoint(3.91,6.91),4326)::geography,'{"name":"PRIVATE_PICKUP"}','{"name":"PRIVATE_DESTINATION"}',$3::uuid,$4)`, id, owner, pricing, market)
		return id
	}
	fare := makeFare(rider)
	if _, err := pool.Exec(ctx, `UPDATE ride_fares SET guest_name='Amina',guest_phone='+2348012345678',ride_preferences='{"conversation":"quiet","air":"either","music":"off"}' WHERE id=$1::uuid`, fare); err != nil {
		t.Fatal(err)
	}
	// A confirmed pin has coordinate metadata, not a resolvable provider ID.
	// Exercise it through booking, readback and the existing dispatch checks.
	exec(`UPDATE ride_fares SET destination_metadata='{"id":"pin:6.9100000,3.9100000","provider":"pin","name":"Pinned destination","address":"","location":{"latitude":6.91,"longitude":3.91}}'::jsonb WHERE id=$1::uuid`, fare)
	for _, id := range []string{rider, other} {
		exec(`UPDATE users SET email=id::text||'@example.test',phone_e164='+23480'||lpad((abs(hashtext(id::text)::bigint)%100000000)::text,8,'0'),phone_verified_at=NOW() WHERE id=$1::uuid`, id)
	}
	request := uuid.NewString()
	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	body := fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, request, fare, at.Format(time.RFC3339))
	for _, field := range []string{"email_verified=FALSE", "phone_verified_at=NULL"} {
		exec(`UPDATE users SET `+field+` WHERE id=$1::uuid`, rider)
		denied := call(rider, "POST", "/rider/scheduled-rides", body)
		if denied.Code != 403 || !strings.Contains(denied.Body.String(), "rider_contact_verification_required") {
			t.Fatal("schedule contact gate", denied.Code, denied.Body.String())
		}
		exec(`UPDATE users SET email_verified=TRUE,phone_verified_at=NOW() WHERE id=$1::uuid`, rider)
	}

	// A quote from another published pricing period must request re-pricing.
	stalePricing := uuid.NewString()
	exec(`INSERT INTO pricing_versions(id,market_code,version,status,effective_from,effective_until,created_by,approved_by,approved_at)VALUES($1::uuid,$2,2,'approved',NOW()-INTERVAL '3 days',NOW()-INTERVAL '2 days',$3::uuid,$4::uuid,NOW())`, stalePricing, market, staff, approver)
	oldPeriodFare := makeFare(rider)
	exec(`UPDATE ride_fares SET pricing_version_id=$2::uuid WHERE id=$1::uuid`, oldPeriodFare, stalePricing)
	stale := call(rider, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), oldPeriodFare, at.Format(time.RFC3339)))
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "fare_requote_required") {
		t.Fatal("stale scheduled fare", stale.Code, stale.Body.String())
	}
	w := call(rider, "POST", "/rider/scheduled-rides", body)
	if w.Code != 200 {
		t.Fatal("book", w.Body.String())
	}
	var response struct {
		Data struct {
			ID string `json:"bookingId"`
		}
	}
	if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	booking := response.Data.ID
	if pinned := call(rider, "GET", "/rider/scheduled-rides/"+booking, ""); pinned.Code != 200 || !strings.Contains(pinned.Body.String(), "Pinned destination") || !strings.Contains(pinned.Body.String(), "pin:6.9100000,3.9100000") {
		t.Fatal("pinned scheduled destination readback", pinned.Code, pinned.Body.String())
	}
	if w = call(rider, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), makeFare(rider), at.Format(time.RFC3339))); w.Code != 409 {
		t.Fatal("overlapping rider booking", w.Body.String())
	}
	if w = call(rider, "POST", "/rider/scheduled-rides", body); w.Code != 200 {
		t.Fatal("retry", w.Body.String())
	}
	if w = call(other, "GET", "/rider/scheduled-rides", ""); w.Code != 200 || strings.Contains(w.Body.String(), booking) {
		t.Fatal("ownership", w.Body.String())
	}
	if w = call(other, "GET", "/rider/scheduled-rides/"+booking, ""); w.Code != 404 {
		t.Fatal("foreign detail", w.Body.String())
	}
	if w = call(driver, "GET", "/driver/scheduled-rides", ""); w.Code != 200 || !strings.Contains(w.Body.String(), booking) || strings.Contains(w.Body.String(), "PRIVATE_PICKUP") || strings.Contains(w.Body.String(), "Amina") || strings.Contains(w.Body.String(), "+2348012345678") {
		t.Fatal("market privacy", w.Body.String())
	}
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("offer delivery", e)
	}
	var notifications int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM driver_notifications WHERE driver_id=$1::uuid AND data->>'bookingId'=$2`, driver, booking).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("notification", notifications, e)
	}
	var wg sync.WaitGroup
	results := make(chan struct {
		driver string
		code   int
	}, 2)
	for _, who := range []string{driver, second} {
		wg.Add(1)
		go func(who string) {
			defer wg.Done()
			results <- struct {
				driver string
				code   int
			}{who, call(who, "POST", "/driver/scheduled-rides/"+booking+"/accept", `{}`).Code}
		}(who)
	}
	wg.Wait()
	close(results)
	winner := ""
	for r := range results {
		if r.code == 200 {
			if winner != "" {
				t.Fatal("two drivers reserved")
			}
			winner = r.driver
		} else if r.code != 409 {
			t.Fatal("accept", r.code)
		}
	}
	if winner == "" {
		t.Fatal("no winner")
	}
	overlapFare := makeFare(other)
	w = call(other, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), overlapFare, at.Format(time.RFC3339)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	overlap := response.Data.ID
	// New bookings have an independent cancellation cutoff and a truthful payment method.
	var cutoff, dispatchAt time.Time
	var payment string
	if e = pool.QueryRow(ctx, `SELECT free_cancellation_until,dispatch_at,payment_method FROM scheduled_rides WHERE id=$1::uuid`, overlap).Scan(&cutoff, &dispatchAt, &payment); e != nil || !cutoff.Equal(at.Add(-time.Hour)) || !dispatchAt.Equal(at.Add(-30*time.Minute)) || payment != "direct_to_driver" {
		t.Fatal("booking policy snapshot", e, cutoff, dispatchAt, payment)
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+overlap+"/cancel", `{"reasonCode":"driver_too_slow"}`); w.Code != 400 {
		t.Fatal("invalid booking reason accepted", w.Body.String())
	}
	if w = call(rider, "PUT", "/rider/scheduled-rides/"+overlap+"/reminder", `{"version":1,"enabled":true}`); w.Code == 200 {
		t.Fatal("foreign reminder updated")
	}
	if w = call(other, "PUT", "/rider/scheduled-rides/"+overlap+"/reminder", `{"version":1,"enabled":true}`); w.Code != 200 {
		t.Fatal("reminder preference", w.Body.String())
	}
	exec(`UPDATE scheduled_rides SET rider_reminder_at=NOW()-INTERVAL '1 minute' WHERE id=$1::uuid`, overlap)
	for i := 0; i < 2; i++ {
		if e = scheduledRiderRemindersOnce(ctx, pool, 100); e != nil {
			t.Fatal(e)
		}
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM rider_notifications WHERE rider_id=$1::uuid AND title='Your scheduled ride is coming up' AND data->'target'->>'id'=$2`, other, overlap).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("reminder deduplication", e, notifications)
	}
	exec(`UPDATE scheduled_rides SET free_cancellation_until=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, overlap)
	if w = call(other, "POST", "/rider/scheduled-rides/"+overlap+"/cancel", `{"reasonCode":"plans_changed"}`); w.Code != 409 {
		t.Fatal("cancellation deadline not enforced", w.Body.String())
	}
	exec(`UPDATE scheduled_rides SET free_cancellation_until=$2 WHERE id=$1::uuid`, overlap, cutoff)
	if w = call(winner, "POST", "/driver/scheduled-rides/"+overlap+"/accept", `{}`); w.Code != 409 {
		t.Fatal("driver double booking", w.Body.String())
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+overlap+"/cancel", `{"reasonCode":"plans_changed"}`); w.Code != 200 {
		t.Fatal("cancel", w.Body.String())
	}
	var reasonCode string
	if e = pool.QueryRow(ctx, `SELECT cancellation_reason_code FROM scheduled_rides WHERE id=$1::uuid`, overlap).Scan(&reasonCode); e != nil || reasonCode != "plans_changed" {
		t.Fatal("reason not persisted", e, reasonCode)
	}
	if w = call(winner, "GET", "/driver/scheduled-rides?view=mine", ""); !strings.Contains(w.Body.String(), "PRIVATE_PICKUP") {
		t.Fatal("reserved details", w.Body.String())
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+booking+"/cancel", `{"reasonCode":"plans_changed"}`); w.Code != 404 {
		t.Fatal("foreign cancellation", w.Body.String())
	}
	repository := triprepo.NewPostgresRepository(pool)
	quoted, e := repository.GetRideFareByID(ctx, fare)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: rider, Status: "pending", RideFare: quoted}); e == nil {
		t.Fatal("scheduled fare allowed instant start")
	}
	// Move the test clock window through readiness, then durable dispatch.
	exec(`UPDATE scheduled_rides SET readiness_at=NOW()-INTERVAL '1 minute',dispatch_at=NOW()+INTERVAL '5 minutes',pickup_at=NOW()+INTERVAL '35 minutes',ends_at=NOW()+INTERVAL '60 minutes' WHERE id=$1::uuid`, booking)
	exec(`UPDATE driver_live_locations SET recorded_at=NOW()-INTERVAL '5 minutes' WHERE driver_id=$1::uuid`, winner)
	if w = call(winner, "POST", "/test/"+booking+"/ready", `{}`); w.Code != 404 {
		t.Fatal("stale readiness", w.Body.String())
	}
	exec(`UPDATE driver_live_locations SET recorded_at=NOW() WHERE driver_id=$1::uuid`, winner)
	if w = call(winner, "POST", "/test/"+booking+"/ready", `{}`); w.Code != 200 {
		t.Fatal("readiness", w.Body.String())
	}
	immediate, e := repository.GetRideFareByID(ctx, makeFare(rider))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repository.Create(ctx, &triptypes.TripModel{ID: uuid.NewString(), RiderID: rider, Status: "pending", RideFare: immediate}); e != triprepo.ErrScheduledConflict {
		t.Fatal("rider immediate-trip conflict", e)
	}
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("reminder", e)
	}
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("reminder retry", e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type='scheduled.event.reminder' AND data->>'bookingId'=$2`, winner, booking).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("reminder dedup", notifications, e)
	}
	exec(`UPDATE scheduled_rides SET dispatch_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, booking)
	var allowed bool
	if e = pool.QueryRow(ctx, `SELECT driver_schedule_allows($1::uuid,NULL)`, winner).Scan(&allowed); e != nil || allowed {
		t.Fatal("ordinary dispatch must respect readiness", allowed, e)
	}
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("dispatch", e)
	}
	var tripID, state, preferred string
	if e = pool.QueryRow(ctx, `SELECT s.status,s.trip_id::text,t.preferred_driver_id::text FROM scheduled_rides s JOIN trips t ON t.id=s.trip_id WHERE s.id=$1::uuid`, booking).Scan(&state, &tripID, &preferred); e != nil || state != "dispatched" || preferred != winner {
		t.Fatal("dispatch link", state, preferred, e)
	}
	var preference string
	if e = pool.QueryRow(ctx, `SELECT ride_preferences->>'conversation' FROM trips WHERE id=$1::uuid`, tripID).Scan(&preference); e != nil || preference != "quiet" {
		t.Fatal("preferences lost at scheduled dispatch", e)
	}
	var guestName, guestPhone string
	if e = pool.QueryRow(ctx, `SELECT guest_name,guest_phone FROM trips WHERE id=$1::uuid`, tripID).Scan(&guestName, &guestPhone); e != nil || guestName != "Amina" || guestPhone != "+2348012345678" {
		t.Fatal("guest lost during scheduled dispatch", e)
	}
	if e = pool.QueryRow(ctx, `SELECT driver_schedule_allows($1::uuid,$2::uuid)`, winner, tripID).Scan(&allowed); e != nil || !allowed {
		t.Fatal("own scheduled dispatch should be allowed", allowed, e)
	}
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("restart recovery", e)
	}
	recovered, e := repository.Create(scheduling.DispatchContext(ctx, booking), &triptypes.TripModel{ID: uuid.NewString(), RiderID: rider, Status: "pending", RideFare: quoted})
	if e != nil || recovered.ID != tripID {
		t.Fatal("idempotent dispatch", e)
	}
	// Driver service must offer the reserved driver first, retaining normal gates.
	payload := fmt.Sprintf(`{"trip":{"id":%q,"riderID":%q,"selectedFare":{"id":%q,"packageSlug":"sedan"},"route":{},"status":"pending"}}`, tripID, rider, fare)
	candidate, e := driverrepo.NewPostgresDriverRepository(pool).MatchAndReserve(ctx, tripID, "sedan", 6.9, 3.9, 15000, time.Minute, []byte(payload))
	if e != nil || candidate.Driver.Id != winner {
		t.Fatal("reserved matching", e)
	}
	exec(`UPDATE trips SET status='arrived',arrived_at=NOW()-INTERVAL '30 minutes' WHERE id=$1::uuid`, tripID)
	exec(`INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,1,3,100,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW()-INTERVAL '1 minute')`, uuid.NewString(), market, staff, approver)
	if w = call(winner, "GET", "/test/no-show/"+tripID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"eligible":false`) || !strings.Contains(w.Body.String(), "wait_period_not_elapsed") {
		t.Fatal("early no-show eligibility", w.Body.String())
	}
	if w = call(winner, "POST", "/test/no-show/"+tripID, `{}`); w.Code != 409 {
		t.Fatal("early no-show claim", w.Body.String())
	}
	if _, changed, e := repository.Start(ctx, tripID, winner); e == nil || changed {
		t.Fatal("scheduled ride started before pickup", e)
	}
	exec(`UPDATE trips SET status='cancelled',cancellation_reason='test' WHERE id=$1::uuid`, tripID)
	if e = pool.QueryRow(ctx, `SELECT status FROM scheduled_rides WHERE id=$1::uuid`, booking).Scan(&state); e != nil || state != "unfulfilled" {
		t.Fatal("terminal synchronization", state, e)
	}
	// A missed readiness confirmation releases the driver and uses live supply.
	exec(`UPDATE drivers SET status='available',available=true WHERE id IN($1::uuid,$2::uuid)`, driver, second)
	otherFare := makeFare(other)
	otherBody := fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), otherFare, at.Add(time.Hour).Format(time.RFC3339))
	w = call(other, "POST", "/rider/scheduled-rides", otherBody)
	if w.Code != 200 {
		t.Fatal("fallback book", w.Body.String())
	}
	if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	fallback := response.Data.ID
	if w = call(winner, "POST", "/driver/scheduled-rides/"+fallback+"/accept", `{}`); w.Code != 200 {
		t.Fatal("fallback accept", w.Body.String())
	}
	exec(`UPDATE scheduled_rides SET readiness_at=NOW()-INTERVAL '45 minutes',dispatch_at=NOW()-INTERVAL '1 minute',pickup_at=NOW()+INTERVAL '29 minutes',ends_at=NOW()+INTERVAL '90 minutes' WHERE id=$1::uuid`, fallback)
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("fallback maintenance", e)
	}
	var driverAfter *string
	if e = pool.QueryRow(ctx, `SELECT status,driver_id::text FROM scheduled_rides WHERE id=$1::uuid`, fallback).Scan(&state, &driverAfter); e != nil || state != "dispatched" || driverAfter != nil {
		t.Fatal("unready reservation was not released", state, driverAfter, e)
	}
	var replacementEvents int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type='scheduled.event.replacement_required' AND data->>'bookingId'=$2`, other, fallback).Scan(&replacementEvents); e != nil || replacementEvents != 1 {
		t.Fatal("replacement notice", replacementEvents, e)
	}
	// A server outage past the grace period must not silently dispatch late.
	expiredFare := makeFare(rider)
	expiredBody := fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), expiredFare, at.Add(2*time.Hour).Format(time.RFC3339))
	w = call(rider, "POST", "/rider/scheduled-rides", expiredBody)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	expired := response.Data.ID
	exec(`UPDATE scheduled_rides SET readiness_at=NOW()-INTERVAL '60 minutes',dispatch_at=NOW()-INTERVAL '45 minutes',pickup_at=NOW()-INTERVAL '6 minutes',ends_at=NOW()+INTERVAL '10 minutes' WHERE id=$1::uuid`, expired)
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `SELECT status FROM scheduled_rides WHERE id=$1::uuid`, expired).Scan(&state); e != nil || state != "unfulfilled" {
		t.Fatal("late dispatch", state, e)
	}
	// Schema evidence plus rollback/idempotency gates for booking edits.
	var reminderColumn bool
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='scheduled_rides' AND column_name='reminder_sent_at')`).Scan(&reminderColumn); e != nil || !reminderColumn {
		t.Fatal("Driver reminder column", e)
	}
	editAt := at.Add(24 * time.Hour)
	w = call(other, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), makeFare(other), editAt.Format(time.RFC3339)))
	if w.Code != 200 {
		t.Fatal("edit fixture", w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	editID := response.Data.ID
	if e = RunScheduledRidesOnce(ctx, pool, 100); e != nil {
		t.Fatal("original edit offers", e)
	}
	decliner := driver
	if decliner == winner {
		decliner = second
	}
	if w = call(decliner, "POST", "/driver/scheduled-rides/"+editID+"/decline", `{}`); w.Code != 200 {
		t.Fatal("original decline", w.Body.String())
	}
	if w = call(winner, "POST", "/driver/scheduled-rides/"+editID+"/accept", `{}`); w.Code != 200 {
		t.Fatal("reserve edit fixture", w.Body.String())
	}
	var editVersion int64
	if e = pool.QueryRow(ctx, `SELECT version FROM scheduled_rides WHERE id=$1::uuid`, editID).Scan(&editVersion); e != nil {
		t.Fatal(e)
	}
	newFare := makeFare(other)
	editRequest := uuid.NewString()
	exec(`UPDATE ride_fares SET total_fare_minor=20000 WHERE id=$1::uuid`, newFare)
	editPayload := func(request, fare string, version int64, when time.Time) string {
		return fmt.Sprintf(`{"requestId":%q,"version":%d,"fareId":%q,"pickupAt":%q,"confirmed":true}`, request, version, fare, when.Format(time.RFC3339))
	}
	editBody := editPayload(editRequest, newFare, editVersion, editAt.Add(time.Hour))
	t.Setenv("HEYGO_SCHEDULE_EDITS_ENABLED", "false")
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editBody); w.Code != 409 || !strings.Contains(w.Body.String(), "schedule_edits_disabled") {
		t.Fatal("rollout gate", w.Body.String())
	}
	t.Setenv("HEYGO_SCHEDULE_EDITS_ENABLED", "true")
	if w = call(rider, "POST", "/rider/scheduled-rides/"+editID+"/amend", editBody); w.Code != 404 {
		t.Fatal("foreign edit", w.Body.String())
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", strings.Replace(editBody, `"confirmed":true`, `"confirmed":false`, 1)); w.Code != 400 {
		t.Fatal("unconfirmed edit accepted", w.Body.String())
	}
	editResults := make(chan *httptest.ResponseRecorder, 3)
	for i := 0; i < 3; i++ {
		go func() { editResults <- call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editBody) }()
	}
	for i := 0; i < 3; i++ {
		if result := <-editResults; result.Code != 200 {
			t.Fatal("concurrent edit/retry", result.Body.String())
		}
	}
	var editedFare string
	if e = pool.QueryRow(ctx, `SELECT status,driver_id::text,fare_id::text,version FROM scheduled_rides WHERE id=$1::uuid`, editID).Scan(&state, &driverAfter, &editedFare, &editVersion); e != nil || state != "open" || driverAfter != nil || editedFare != newFare {
		t.Fatal("edited state", e, state, driverAfter, editedFare)
	}
	if w = call(other, "GET", "/rider/scheduled-rides/"+editID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"fareKobo":20000`) {
		t.Fatal("replacement fare response", w.Body.String())
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type='scheduled.event.amended' AND data->>'bookingId'=$2`, winner, editID).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("reservation release event", e, notifications)
	}
	for i := 0; i < 2; i++ {
		if e = scheduledEditedOffersOnce(ctx, pool, 100); e != nil {
			t.Fatal("revised offers", e)
		}
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE recipient_id=$1::uuid AND type='scheduled.event.offer' AND data->>'bookingId'=$2 AND data->>'bookingChanged'='true'`, decliner, editID).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("revised offer deduplication", e, notifications)
	}
	if w = call(decliner, "GET", "/driver/scheduled-rides?view=available", ""); w.Code != 200 || !strings.Contains(w.Body.String(), editID) {
		t.Fatal("old decline hid revised booking", w.Body.String())
	}
	if w = call(decliner, "POST", "/driver/scheduled-rides/"+editID+"/decline", `{}`); w.Code != 200 {
		t.Fatal("revised decline", w.Body.String())
	}
	if w = call(decliner, "GET", "/driver/scheduled-rides?view=available", ""); strings.Contains(w.Body.String(), editID) {
		t.Fatal("revised decline not respected", w.Body.String())
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_ride_declines WHERE booking_id=$1::uuid AND driver_id=$2::uuid`, editID, decliner).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("old decline history removed", e)
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(editRequest, newFare, editVersion, editAt.Add(2*time.Hour))); w.Code != 409 {
		t.Fatal("changed retry accepted")
	}
	// Both ownership/expiry failures and commitment conflicts must retain the
	// original booking and leave the replacement quote unconsumed.
	staleFare := makeFare(other)
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(uuid.NewString(), staleFare, editVersion-1, editAt.Add(2*time.Hour))); w.Code != 409 {
		t.Fatal("stale version accepted")
	}
	foreignFare := makeFare(rider)
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(uuid.NewString(), foreignFare, editVersion, editAt.Add(2*time.Hour))); w.Code != 409 {
		t.Fatal("foreign fare accepted")
	}
	exec(`UPDATE ride_fares SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, staleFare)
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(uuid.NewString(), staleFare, editVersion, editAt.Add(2*time.Hour))); w.Code != 409 {
		t.Fatal("expired fare accepted")
	}
	conflictFare := makeFare(other)
	blockedAt := editAt.Add(4 * time.Hour)
	if w = call(other, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), makeFare(other), blockedAt.Format(time.RFC3339))); w.Code != 200 {
		t.Fatal("overlap fixture", w.Body.String())
	}
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(uuid.NewString(), conflictFare, editVersion, blockedAt)); w.Code != 409 {
		t.Fatal("overlapping commitment accepted", w.Body.String())
	}
	var consumed bool
	if e = pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM ride_fares WHERE id=$1::uuid`, conflictFare).Scan(&consumed); e != nil || consumed {
		t.Fatal("failed edit consumed quote", e)
	}
	if e = pool.QueryRow(ctx, `SELECT fare_id::text FROM scheduled_rides WHERE id=$1::uuid`, editID).Scan(&editedFare); e != nil || editedFare != newFare {
		t.Fatal("failed edit changed booking", e)
	}
	exec(`UPDATE scheduled_rides SET free_cancellation_until=NOW()-INTERVAL '1 second' WHERE id=$1::uuid`, editID)
	if w = call(other, "POST", "/rider/scheduled-rides/"+editID+"/amend", editPayload(uuid.NewString(), conflictFare, editVersion, editAt.Add(3*time.Hour))); w.Code != 409 {
		t.Fatal("edit after deadline accepted")
	}
	policy, e := readSchedulePolicy(ctx, pool)
	if e != nil {
		t.Fatal(e)
	}
	policy.Enabled = false
	policyJSON, _ := json.Marshal(policy)
	if w = call(rider, "PUT", "/test/scheduling/settings", string(policyJSON)); w.Code != 200 {
		t.Fatal("admin policy", w.Body.String())
	}
	if w = call(rider, "PUT", "/test/scheduling/settings", string(policyJSON)); w.Code != 409 {
		t.Fatal("stale policy", w.Body.String())
	}
	if w = call(rider, "POST", "/rider/scheduled-rides", fmt.Sprintf(`{"requestId":%q,"fareId":%q,"pickupAt":%q,"confirmed":true}`, uuid.NewString(), makeFare(rider), at.Add(5*time.Hour).Format(time.RFC3339))); w.Code != 409 {
		t.Fatal("disabled booking", w.Body.String())
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE admin_id=$1::uuid AND action='scheduling.policy.created'`, staff).Scan(&notifications); e != nil || notifications != 1 {
		t.Fatal("policy audit", notifications, e)
	}
}

func TestSchedulePolicyValidation(t *testing.T) {
	p := schedulePolicy{Version: 1, MinAdvance: 60, MaxAdvance: 10080, Dispatch: 30, Readiness: 45, FreeCancellation: 60, PickupWindow: 5, RiderReminder: 60}
	if !validSchedulePolicy(p) {
		t.Fatal("default")
	}
	p.Enabled = true
	if validSchedulePolicy(p) {
		t.Fatal("enable without coverage")
	}
	p.Markets = []string{"LAGOS"}
	p.Categories = []string{"sedan"}
	if !validSchedulePolicy(p) {
		t.Fatal("enabled")
	}
	p.Readiness = 20
	if validSchedulePolicy(p) {
		t.Fatal("readiness after dispatch")
	}
	if adminRoutePermission("PUT", "/admin/scheduling/settings") != permissionSettings {
		t.Fatal("policy permission")
	}
	arrived := time.Now()
	pickup := arrived.Add(time.Hour)
	if !scheduledNoShowDeadline(arrived, &pickup, 3).Equal(pickup.Add(3 * time.Minute)) {
		t.Fatal("early arrival no-show")
	}
	if !scheduledNoShowDeadline(pickup, &arrived, 3).Equal(pickup.Add(3 * time.Minute)) {
		t.Fatal("late arrival no-show")
	}
}
