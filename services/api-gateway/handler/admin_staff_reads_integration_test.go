package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestStaffReadPermissionsAndMinimalRiderResponse(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	staffID, riderID, driverID, fareID, tripID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	username := "read-test-" + staffID
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','support')`, staffID, username); err != nil {
		t.Fatal(err)
	}
	phone := fmt.Sprintf("+234%010d", uint64(time.Now().UnixNano())%10000000000)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,phone_e164) VALUES($1::UUID,$2,$3,ARRAY['rider'],$4)`, riderID, "staff-read:"+riderID, "staff-read-human:"+riderID, phone); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "staff-read:"+driverID, "staff-read-human:"+driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Read Driver','PRIVATE-PLATE','sedan','offline')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_vehicles(driver_id,package_slug,make,model,plate) VALUES($1::UUID,'sedan','Toyota','Corolla','PRIVATE-PLATE')`, driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',180000,'{"routes":[{"distance":1200,"duration":300,"geometry":{"secret":"route"}}],"private":"route"}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, tripID, riderID, fareID); err != nil {
		t.Fatal(err)
	}
	token := "test-token-" + uuid.NewString() + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2::UUID,NOW()+INTERVAL '1 hour')`, adminTokenHash(token), staffID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM driver_vehicles WHERE driver_id=$1::UUID`, driverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM driver_profiles WHERE driver_id=$1::UUID`, driverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM drivers WHERE id=$1::UUID`, driverID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_sessions WHERE admin_id=$1::UUID`, staffID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id=$1::UUID`, staffID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID)`, riderID, driverID)
	}()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := &adminAPI{pool: pool}
	admin := router.Group("/admin", api.authenticate, api.authorize)
	admin.GET("/riders/:riderID", api.staffRiderDetail)
	admin.GET("/riders", api.listStaffRiders)
	admin.GET("/trips", api.listStaffTrips)
	admin.GET("/trips/:tripID", api.staffTripDetail)
	admin.GET("/drivers/:driverID", api.driverReview)
	call := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		router.ServeHTTP(rec, req)
		return rec
	}
	rider := call("/admin/riders/" + riderID)
	if rider.Code != http.StatusOK {
		t.Fatalf("rider status=%d body=%s", rider.Code, rider.Body.String())
	}
	var response struct {
		Data staffRider `json:"data"`
	}
	if err := json.Unmarshal(rider.Body.Bytes(), &response); err != nil || response.Data.ID != riderID {
		t.Fatalf("rider response=%s err=%v", rider.Body.String(), err)
	}
	if strings.Contains(rider.Body.String(), "phone") || strings.Contains(rider.Body.String(), "human") {
		t.Fatalf("private data leaked: %s", rider.Body.String())
	}
	search := call("/admin/riders?q=" + tripID)
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), riderID) {
		t.Fatalf("linked Rider search status=%d body=%s", search.Code, search.Body.String())
	}
	badSearch := call("/admin/riders?q=phone-number")
	if badSearch.Code != http.StatusBadRequest {
		t.Fatalf("contact search should be rejected: %d", badSearch.Code)
	}
	tripList := call("/admin/trips?riderId=" + riderID + "&status=pending")
	if tripList.Code != http.StatusOK || !strings.Contains(tripList.Body.String(), tripID) || strings.Contains(tripList.Body.String(), "private") {
		t.Fatalf("minimal trip response status=%d body=%s", tripList.Code, tripList.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE trips SET assigned_driver_id=$2::UUID,status='accepted',updated_at=NOW() WHERE id=$1::UUID`, tripID, driverID); err != nil {
		t.Fatal(err)
	}
	tripDetail := call("/admin/trips/" + tripID)
	if tripDetail.Code != http.StatusOK || !strings.Contains(tripDetail.Body.String(), `"distanceMeters":1200`) || !strings.Contains(tripDetail.Body.String(), `"durationSeconds":300`) || !strings.Contains(tripDetail.Body.String(), `"complete_since_create"`) || !strings.Contains(tripDetail.Body.String(), `"PRIVATE-PLATE"`) || strings.Contains(tripDetail.Body.String(), `"private"`) || strings.Contains(tripDetail.Body.String(), `"geometry"`) {
		t.Fatalf("safe Trip detail status=%d body=%s", tripDetail.Code, tripDetail.Body.String())
	}
	denied := call("/admin/drivers/" + riderID)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("driver read status=%d body=%s", denied.Code, denied.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET role='finance' WHERE id=$1::UUID`, staffID); err != nil {
		t.Fatal(err)
	}
	financeTrip := call("/admin/trips/" + tripID)
	if financeTrip.Code != http.StatusOK || !strings.Contains(financeTrip.Body.String(), `"policyCharges":[]`) || !strings.Contains(financeTrip.Body.String(), `"debtPaymentStates":[]`) || strings.Contains(financeTrip.Body.String(), `"supportCases"`) || strings.Contains(financeTrip.Body.String(), `"PRIVATE-PLATE"`) {
		t.Fatalf("Finance Trip links status=%d body=%s", financeTrip.Code, financeTrip.Body.String())
	}
	financeRider := call("/admin/riders/" + riderID)
	if financeRider.Code != http.StatusOK || !strings.Contains(financeRider.Body.String(), `"debts":[]`) || strings.Contains(financeRider.Body.String(), `"supportCases"`) {
		t.Fatalf("Finance Rider links status=%d body=%s", financeRider.Code, financeRider.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET role=NULL WHERE id=$1::UUID`, staffID); err != nil {
		t.Fatal(err)
	}
	denied = call("/admin/riders/" + riderID)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unassigned role status=%d body=%s", denied.Code, denied.Body.String())
	}
}
