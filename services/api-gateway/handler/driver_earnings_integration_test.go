package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type earningsTestUsers struct{ id string }

func (s earningsTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"driver"}}, nil
}
func (earningsTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestDriverEarningsPeriodTotalsAndComparison(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driver, rider, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fareIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	tripIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM driver_trip_earnings WHERE trip_id=ANY($1::UUID[])`, tripIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM trips WHERE id=ANY($1::UUID[])`, tripIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM ride_fares WHERE id=ANY($1::UUID[])`, fareIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM drivers WHERE id IN ($1::UUID,$2::UUID)`, driver, other)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID,$3::UUID)`, driver, rider, other)
	})
	for _, u := range []struct{ id, role string }{{driver, "driver"}, {rider, "rider"}, {other, "driver"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4])`, u.id, "earnings-test:"+u.id, "earnings-human:"+u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::UUID,'Earnings Driver','EARN-1','sedan','offline'),($2::UUID,'Other Earnings Driver','EARN-2','sedan','offline')`, driver, other); err != nil {
		t.Fatal(err)
	}
	for _, fareID := range fareIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}'::JSONB)`, fareID, rider); err != nil {
			t.Fatal(err)
		}
	}
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	window, err := earningsWindowFor("week", time.Now(), location)
	if err != nil {
		t.Fatal(err)
	}
	currentAt := window.CurrentStart.AddDate(0, 0, 1).Add(time.Hour)
	priorAt := window.PriorStart.AddDate(0, 0, 1).Add(time.Hour)
	otherAt := window.CurrentStart.AddDate(0, 0, 2).Add(time.Hour)
	for i, item := range []struct {
		at                         time.Time
		owner                      string
		fare, commission, earnings int64
	}{{currentAt, driver, 600000, 0, 600000}, {priorAt, driver, 400000, 0, 400000}, {otherAt, other, 900000, 0, 900000}} {
		if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,created_at,updated_at,completed_at) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'completed',$5,$5,$5)`, tripIDs[i], rider, fareIDs[i], item.owner, item.at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO driver_trip_earnings(trip_id,driver_id,fare_kobo,commission_bps,commission_kobo,earnings_kobo,accrued_at) VALUES($1::UUID,$2::UUID,$3,0,$4,$5,$6)`, tripIDs[i], item.owner, item.fare, item.commission, item.earnings, item.at); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	authenticated := router.Group("/", gatewayauth.NewMiddleware(contractTestVerifier{}, earningsTestUsers{id: driver}).Authenticate)
	registerDriverRoutes(authenticated, pool, nil)
	request := httptest.NewRequest(http.MethodGet, "/driver/earnings?period=week", nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("earnings status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data driverEarningsResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Data
	if got.Period != "week" || got.TimeZone != driverEarningsTimeZone || got.Currency != "NGN" || got.Totals.AmountKobo != 600000 || got.Totals.FareKobo != 600000 || got.Totals.BonusKobo != 0 || got.Totals.CompletedTrips != 1 || got.Comparison.AmountKobo != 400000 || got.Comparison.CompletedTrips != 1 || got.ChangePercent == nil || *got.ChangePercent != 50 {
		t.Fatalf("unexpected earnings response: %+v", got)
	}
	if len(got.Series) != 7 || len(got.ComparisonSeries) != 7 || got.Series[1].AmountKobo != 600000 || got.ComparisonSeries[1].AmountKobo != 400000 {
		t.Fatalf("unexpected chart series: current=%+v prior=%+v", got.Series, got.ComparisonSeries)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/driver/earnings?period=year", nil)
	invalid.Header.Set("Authorization", "Bearer test")
	bad := httptest.NewRecorder()
	router.ServeHTTP(bad, invalid)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid period status=%d body=%s", bad.Code, bad.Body.String())
	}
}
