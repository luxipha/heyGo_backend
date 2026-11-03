package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/heroes"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHeroesAuthAttributionAndAtomicRewards(t *testing.T) {
	database := os.Getenv("HEROES_TEST_DATABASE_URL")
	if database == "" {
		database = os.Getenv("TEST_DATABASE_URL")
	}
	if database == "" {
		t.Skip("Disposable PostgreSQL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: database, MaxConns: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	a := registerHeroesRoutes(r, pool)
	message := ""
	a.pepper = []byte("heroes-test-pepper-32-bytes-long!!")
	a.email.staffEmailSender = func(_ context.Context, _, _, body string) error { message = body; return nil }
	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("Invalid response %s", w.Body.String())
		}
		return w.Code, out
	}
	require := func(want, got int, out any) {
		t.Helper()
		if want != got {
			t.Fatalf("want %d got %d: %v", want, got, out)
		}
	}
	first := uuid.NewString() + "@example.test"
	second := uuid.NewString() + "@example.test"

	code, out := call("POST", "/heroes/auth/otp/request", "", map[string]string{"email": "invalid"})
	require(400, code, out)
	signup := func(email string) string {
		c, b := call("POST", "/heroes/auth/otp/request", "", map[string]string{"email": email})
		require(200, c, b)
		challenge := b["data"].(map[string]any)["challengeId"].(string)
		parts := strings.Fields(message)
		otp := strings.TrimSuffix(parts[5], ".")
		c, b = call("POST", "/heroes/auth/otp/request", "", map[string]string{"email": email})
		require(429, c, b)
		c, b = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": "xxxxxx"})
		require(400, c, b)

		c, b = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": otp})
		require(200, c, b)
		token := b["data"].(map[string]any)["token"].(string)
		c, b = call("GET", "/heroes/me", token, nil)
		require(200, c, b)
		if b["data"].(map[string]any)["needsName"] != true {
			t.Fatal("New Hero did not need a name")
		}
		c, b = call("POST", "/heroes/profile", token, map[string]string{"name": "Ada Hero"})
		require(200, c, b)
		return token
	}
	token := signup(first)
	other := signup(second)
	code, out = call("GET", "/heroes/me", token, nil)
	require(200, code, out)
	me := out["data"].(map[string]any)
	hero := me["id"].(string)
	referral := me["code"].(string)
	code, out = call("GET", "/heroes/me", "", nil)
	require(401, code, out)
	phone := fmt.Sprintf("+23480%08d", uint64(time.Now().UnixNano())%100000000)
	code, out = call("POST", "/heroes/drivers", token, map[string]string{"name": "Tunde Driver", "phone": phone})
	require(200, code, out)
	code, out = call("GET", "/heroes/drivers", other, nil)
	require(200, code, out)
	if len(out["data"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("Another Hero can read leads")
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	staff, driver, rider := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	admin := &adminAPI{pool: pool}
	r.PUT("/test/policy", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff}); admin.saveHeroesPolicy(c) })
	var version int64
	if pool.QueryRow(ctx, `SELECT max(version)FROM heroes_policies`).Scan(&version) != nil {
		t.Fatal("policy missing")
	}
	policy := heroesPolicy{Version: version, Enabled: true, TripTarget: 2, RewardKobo: 250000, Ranks: map[string]int{"E": 0, "D": 1, "C": 2, "B": 3, "A": 4}}
	code, out = call("PUT", "/test/policy", "", policy)
	require(200, code, out)
	code, out = call("PUT", "/test/policy", "", policy)
	require(409, code, out)
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,email)VALUES($1::uuid,$1,$1,ARRAY['driver'],$2),($3::uuid,$3,$3,ARRAY['rider'],NULL)`, driver, uuid.NewString()+"@example.test", rider)
	if heroes.Claim(ctx, pool, driver, referral) == nil {
		t.Fatal("Unverified Driver claimed attribution")
	}
	exec(`UPDATE users SET phone_e164=$2,phone_verified_at=now()WHERE id=$1::uuid`, driver, phone)
	if err = heroes.Claim(ctx, pool, driver, referral); err != nil {
		t.Fatal(err)
	}
	if err = heroes.Claim(ctx, pool, driver, referral); err != nil {
		t.Fatal("Same claim retry failed", err)
	}
	code, out = call("GET", "/heroes/me", other, nil)
	require(200, code, out)
	otherCode := out["data"].(map[string]any)["code"].(string)
	if heroes.Claim(ctx, pool, driver, otherCode) == nil {
		t.Fatal("Another Hero stole attribution")
	}
	exec(`INSERT INTO driver_profiles(driver_id,manual_verification_status,admin_status,admin_reviewed_at)VALUES($1::uuid,'approved','approved',now()+interval '1 hour')`, driver)
	exec(`INSERT INTO driver_vehicles(driver_id,package_slug,review_status,approval_status,approved_at)VALUES($1::uuid,'sedan','approved','approved',now()+interval '1 hour')`, driver)
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at)VALUES($1::uuid,$2::uuid,$3,'approved','test',now()+interval '1 day')`, uuid.NewString(), driver, kind)
	}
	exec(`INSERT INTO driver_inspections(id,driver_id,status)VALUES($1::uuid,$2::uuid,'completed')`, uuid.NewString(), driver)
	fare := uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	addTrip := func(status string, old bool) {
		created := time.Now().Add(time.Second)
		if old {
			created = time.Now().Add(-24 * time.Hour)
		}
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6)`, uuid.NewString(), rider, fare, driver, status, created)
	}
	addTrip("completed", true)
	addTrip("cancelled", false)
	addTrip("completed", false)
	if err = heroes.ProcessRewards(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var balance int64
	readBalance := func() int64 {
		t.Helper()
		if pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_kobo),0)FROM heroes_wallet_entries WHERE hero_id=$1::uuid`, hero).Scan(&balance) != nil {
			t.Fatal("wallet query")
		}
		return balance
	}
	if readBalance() != 0 {
		t.Fatal("Old/cancelled trips incorrectly qualified reward")
	}
	addTrip("completed", false)
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- heroes.ProcessRewards(ctx, pool) }()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	if readBalance() != 250000 {
		t.Fatal("Reward duplicated or missing", balance)
	}
	if _, err = pool.Exec(ctx, `UPDATE heroes_wallet_entries SET amount_kobo=1 WHERE hero_id=$1::uuid`, hero); err == nil {
		t.Fatal("Wallet entry was editable")
	}
	code, out = call("GET", "/heroes/me", token, nil)
	require(200, code, out)
	if out["data"].(map[string]any)["rank"] != "D" {
		t.Fatal("Rank did not reflect onboarding", out)
	}

	// Returning Heroes reuse their account and never repeat name onboarding.
	exec(`UPDATE heroes_email_otps SET requested_at=now()-interval '2 minutes' WHERE email=$1`, first)
	code, out = call("POST", "/heroes/auth/otp/request", "", map[string]string{"email": first})
	require(200, code, out)
	challenge := out["data"].(map[string]any)["challengeId"].(string)
	otp := strings.TrimSuffix(strings.Fields(message)[5], ".")
	code, out = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": otp})
	require(200, code, out)
	fresh := out["data"].(map[string]any)["token"].(string)
	code, out = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": otp})
	require(400, code, out)
	code, out = call("GET", "/heroes/me", fresh, nil)
	require(200, code, out)
	if out["data"].(map[string]any)["needsName"] != false || out["data"].(map[string]any)["id"] != hero {
		t.Fatal("Returning Hero lost identity or name")
	}

	// Five wrong codes exhaust the challenge even if a valid code is later supplied.
	exec(`UPDATE heroes_email_otps SET requested_at=now()-interval '2 minutes' WHERE email=$1`, first)
	code, out = call("POST", "/heroes/auth/otp/request", "", map[string]string{"email": first})
	require(200, code, out)
	challenge = out["data"].(map[string]any)["challengeId"].(string)
	otp = strings.TrimSuffix(strings.Fields(message)[5], ".")
	for i := 0; i < 5; i++ {
		code, out = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": "xxxxxx"})
		require(400, code, out)
	}
	code, out = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": otp})
	require(400, code, out)
	exec(`UPDATE heroes_email_otps SET attempts=0,expires_at=now()-interval '1 second' WHERE email=$1`, first)
	code, out = call("POST", "/heroes/auth/otp/verify", "", map[string]string{"challengeId": challenge, "code": otp})
	require(400, code, out)
	code, out = call("POST", "/heroes/auth/logout", fresh, map[string]any{})
	require(200, code, out)
	code, out = call("GET", "/heroes/me", fresh, nil)
	require(401, code, out)
}
func TestHeroesPolicyValidation(t *testing.T) {
	p := heroesPolicy{TripTarget: 70, RewardKobo: 0, Ranks: map[string]int{"E": 0}}
	if !validHeroesPolicy(p) {
		t.Fatal("Disabled default should be valid")
	}
	p.Enabled = true
	if validHeroesPolicy(p) {
		t.Fatal("Enabled reward cannot be zero")
	}
	p.RewardKobo = 100
	p.Ranks = map[string]int{"E": 0, "D": 10, "C": 5, "B": 30, "A": 40}
	if validHeroesPolicy(p) {
		t.Fatal("Non-increasing ranks accepted")
	}
}
