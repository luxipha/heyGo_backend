package handler

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestGoCoinWalletContracts(t *testing.T) {
	dsn := os.Getenv("GOCOIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable GoCoin database required")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
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
	owner, driver, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, driver, other} {
		exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider','driver'])`, id)
	}
	var version int64
	exec(`INSERT INTO gocoin_policies(earning_enabled,redemption_enabled,coins_per_block,credit_kobo_per_block,categories)VALUES(true,true,10,5000,'{"sedan":{"enabled":true,"coinsPerRide":100}}')`)
	if e = pool.QueryRow(ctx, `SELECT max(version) FROM gocoin_policies`).Scan(&version); e != nil {
		t.Fatal(e)
	}
	makeTrip := func(rider string) (string, string) {
		t.Helper()
		fare, trip := uuid.NewString(), uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, fare, rider)
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status,assigned_driver_id)VALUES($1::uuid,$2::uuid,$3::uuid,'completed',$4::uuid)`, trip, rider, fare, driver)
		return fare, trip
	}
	_, trip := makeTrip(owner)
	exec(`INSERT INTO gocoin_policies DEFAULT VALUES`)
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status,resolved_at)VALUES($1::uuid,$2::uuid,$3::uuid,10000,'pending',NULL)`, trip, driver, owner)
	var balance int64
	if e = pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid),0)`, owner).Scan(&balance); e != nil || balance != 0 {
		t.Fatalf("pending %d %v", balance, e)
	}
	exec(`UPDATE trip_settlements SET status='confirmed',resolved_at=NOW() WHERE trip_id=$1::uuid`, trip)
	exec(`UPDATE trip_settlements SET status='confirmed' WHERE trip_id=$1::uuid`, trip)
	if e = pool.QueryRow(ctx, `SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid`, owner).Scan(&balance); e != nil || balance != 100 {
		t.Fatalf("frozen reward %d %v", balance, e)
	}
	// A fare booked while disabled never becomes retrospectively eligible.
	_, disabledTrip := makeTrip(owner)
	exec(`INSERT INTO gocoin_policies(earning_enabled,redemption_enabled,coins_per_block,credit_kobo_per_block,categories)VALUES(true,true,10,5000,'{"sedan":{"enabled":true,"coinsPerRide":200}}')`)
	if e = pool.QueryRow(ctx, `SELECT max(version) FROM gocoin_policies`).Scan(&version); e != nil {
		t.Fatal(e)
	}
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status,resolved_at)VALUES($1::uuid,$2::uuid,$3::uuid,10000,'confirmed',NOW())`, disabledTrip, driver, owner)
	_, selfTrip := makeTrip(driver)
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status,resolved_at)VALUES($1::uuid,$2::uuid,$2::uuid,10000,'confirmed',NOW())`, selfTrip, driver)
	_, earlyPaid := makeTrip(other)
	exec(`UPDATE trips SET status='started' WHERE id=$1::uuid`, earlyPaid)
	exec(`INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,status,resolved_at)VALUES($1::uuid,$2::uuid,$3::uuid,10000,'confirmed',NOW())`, earlyPaid, driver, other)
	if e = pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid),0)`, other).Scan(&balance); e != nil || balance != 0 {
		t.Fatal("payment before completion must wait", balance, e)
	}
	exec(`UPDATE trips SET status='completed' WHERE id=$1::uuid`, earlyPaid)
	if e = pool.QueryRow(ctx, `SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid`, other).Scan(&balance); e != nil || balance != 200 {
		t.Fatal("completion after payment", balance, e)
	}
	router := gin.New()
	group := router.Group("", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Owner"), Roles: []string{"rider"}})
	})
	registerRiderWalletRoutes(group, pool)
	staff := uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	admin := &adminAPI{pool: pool}
	router.PUT("/test/gocoin/settings", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff}); admin.saveCoinPolicy(c) })
	call := func(user, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Owner", user)
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	request := uuid.NewString()
	body := fmt.Sprintf(`{"requestId":%q,"coins":20,"policyVersion":%d}`, request, version)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- call(owner, "POST", "/rider/gocoin/redeem", body).Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("retry %d", code)
		}
	}
	if e = pool.QueryRow(ctx, `SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid`, owner).Scan(&balance); e != nil || balance != 80 {
		t.Fatalf("coins %d %v", balance, e)
	}
	if e = pool.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid`, owner).Scan(&balance); e != nil || balance != 10000 {
		t.Fatalf("credit %d %v", balance, e)
	}
	if w := call(other, "GET", "/rider/credits/transactions", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Body.String())
	}
	if w := call(owner, "GET", "/rider/gocoin/transactions?limit=1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"beforeId"`) {
		t.Fatal(w.Body.String())
	}
	if w := call(owner, "POST", "/rider/gocoin/redeem", fmt.Sprintf(`{"requestId":%q,"coins":1000,"policyVersion":%d}`, uuid.NewString(), version)); w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	if w := call(owner, "POST", "/rider/gocoin/redeem", strings.Replace(body, `"coins":20`, `"coins":30`, 1)); w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	if _, e = pool.Exec(ctx, `UPDATE gocoin_entries SET delta=1 WHERE rider_id=$1::uuid`, owner); e == nil {
		t.Fatal("ledger allowed mutation")
	}
	policyBody := fmt.Sprintf(`{"version":%d,"earningEnabled":false,"redemptionEnabled":false,"coinsPerBlock":10,"creditKoboPerBlock":5000,"categories":{}}`, version)
	if w := call(owner, "PUT", "/test/gocoin/settings", policyBody); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(owner, "PUT", "/test/gocoin/settings", policyBody); w.Code != 409 {
		t.Fatal("stale policy", w.Body.String())
	}
	if w := call(owner, "POST", "/rider/gocoin/redeem", body); w.Code != 200 {
		t.Fatal("completed request must remain retryable after disabling", w.Body.String())
	}
	if w := call(owner, "POST", "/rider/gocoin/redeem", fmt.Sprintf(`{"requestId":%q,"coins":10,"policyVersion":%d}`, uuid.NewString(), version+1)); w.Code != 409 {
		t.Fatal("disabled redemption", w.Body.String())
	}
	var audits int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE action='gocoin.policy.created' AND admin_id=$1::uuid`, staff).Scan(&audits); e != nil {
		t.Fatal(e)
	} else if audits != 1 {
		t.Fatal("policy audit", audits)
	}
}

func TestCoinPolicyValidation(t *testing.T) {
	if adminRoutePermission("PUT", "/admin/gocoin/settings") != permissionSettings {
		t.Fatal("policy RBAC")
	}
	p := coinPolicy{Version: 1, RedemptionEnabled: true, CoinsPerBlock: 10, CreditKoboPerBlock: 100, Categories: map[string]coinRule{"sedan": {Enabled: true, CoinsPerRide: 5}}}
	if !validCoinPolicy(p) {
		t.Fatal("valid policy")
	}
	p.CoinsPerBlock = 0
	if validCoinPolicy(p) {
		t.Fatal("zero conversion")
	}
	p.RedemptionEnabled = false
	p.Categories["sedan"] = coinRule{Enabled: true, CoinsPerRide: 0}
	if validCoinPolicy(p) {
		t.Fatal("zero enabled reward")
	}
}
