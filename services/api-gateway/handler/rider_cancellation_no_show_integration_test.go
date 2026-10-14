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

func TestRiderCancellationAfterArrivalReusesReviewedNoShowClaim(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rider, driver, admin1, admin2, policy := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "CANCEL_" + strings.ToUpper(policy[:8])
	for _, u := range []struct{ id, role string }{{rider, "rider"}, {driver, "driver"}} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "cancel:"+u.id, "cancel:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{admin1, admin2} {
		if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, id, "cancel-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested,online_market_code) VALUES($1::UUID,'Driver','TEST','sedan','offline',FALSE,$2)`, driver, market); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by,approved_by,approved_at)
		VALUES($1::UUID,$2,1,5,5000,'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW()-INTERVAL '1 day')`, policy, market, admin1, admin2); err != nil {
		t.Fatal(err)
	}
	api := &driverAPI{pool: pool}
	for _, tc := range []struct {
		name           string
		arrivedMinutes int
		wantStatus     int
		wantEligible   bool
	}{{"after_wait", 10, http.StatusAccepted, true}, {"before_wait", 3, http.StatusConflict, false}} {
		t.Run(tc.name, func(t *testing.T) {
			tripID, fareID := uuid.NewString(), uuid.NewString()
			if _, err = pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}'::JSONB)`, fareID, rider); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,market_code,arrived_at,cancelled_at,cancellation_actor_id,cancellation_reason)
				VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'cancelled',$5,NOW()-($6::INT*INTERVAL '1 minute'),NOW()-INTERVAL '1 minute',$2::UUID,'rider_cancelled')`, tripID, rider, fareID, driver, market, tc.arrivedMinutes); err != nil {
				t.Fatal(err)
			}
			invoke := func(method string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(method, "/driver/trips/"+tripID+"/no-show", nil)
				c.Params = gin.Params{{Key: "tripID", Value: tripID}}
				c.Set("authenticated-user", gatewayauth.User{ID: driver, Roles: []string{"driver"}})
				handler(c)
				return rec
			}
			eligibility := invoke(http.MethodGet, api.noShowEligibility)
			if eligibility.Code != http.StatusOK || strings.Contains(eligibility.Body.String(), `"eligible":true`) != tc.wantEligible {
				t.Fatalf("eligibility status=%d body=%s", eligibility.Code, eligibility.Body.String())
			}
			claim := invoke(http.MethodPost, api.submitNoShowClaim)
			if claim.Code != tc.wantStatus {
				t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
			}
			var debts int
			if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM rider_no_show_debts d JOIN driver_no_show_claims c ON c.id=d.claim_id WHERE c.trip_id=$1::UUID`, tripID).Scan(&debts); err != nil || debts != 0 {
				t.Fatalf("debt created before Finance review: %d %v", debts, err)
			}
		})
	}
}
