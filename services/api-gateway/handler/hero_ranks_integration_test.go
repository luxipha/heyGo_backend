package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestHeroRankTripMinimumValidation(t *testing.T) {
	p := heroesPolicy{TripTarget: 70, Ranks: map[string]int{"E": 0}}
	for _, minimum := range []int{0, 10, 10000} {
		p.RankMinimumTrips = minimum
		if !validHeroesPolicy(p) {
			t.Fatalf("valid minimum %d rejected", minimum)
		}
	}
	for _, minimum := range []int{-1, 10001} {
		p.RankMinimumTrips = minimum
		if validHeroesPolicy(p) {
			t.Fatalf("invalid minimum %d accepted", minimum)
		}
	}
}

func TestHeroRanksCountDriversMeetingTripMinimum(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	hero, legacy, programme, unapproved, rider, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO heroes_accounts(id,email,name,referral_code)VALUES($1::uuid,$1||'@example.test','Rank Hero',$1)`, hero)
	exec(`INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['driver']),($2::uuid,ARRAY['driver']),($3::uuid,ARRAY['driver']),($4::uuid,ARRAY['rider'])`, legacy, programme, unapproved, rider)
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO heroes_referrals(id,hero_id,driver_id,policy_id,claimed_at,onboarded_at)SELECT $1::uuid,$2::uuid,$3::uuid,id,now()-interval '1 day',now() FROM heroes_policies ORDER BY version DESC LIMIT 1`, uuid.NewString(), hero, legacy)
	exec(`INSERT INTO referral_attributions(id,hero_id,invitee_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target,linked_at,deadline_at,onboarded_at)SELECT $1::uuid,$2::uuid,$3::uuid,'hero','driver',version,700000,10,now()-interval '1 day',now()-interval '1 hour',now() FROM referral_settings ORDER BY version DESC LIMIT 1`, uuid.NewString(), hero, programme)
	exec(`INSERT INTO heroes_referrals(id,hero_id,driver_id,policy_id,claimed_at)SELECT $1::uuid,$2::uuid,$3::uuid,id,now()-interval '1 day' FROM heroes_policies ORDER BY version DESC LIMIT 1`, uuid.NewString(), hero, unapproved)
	fare := uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	add := func(driver string, completed time.Time) {
		t.Helper()
		exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,created_at,completed_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'completed',now()-interval '2 hours',$5)`, uuid.NewString(), rider, driver, fare, completed)
	}
	beforeDeadline := time.Now().Add(-2 * time.Hour)
	add(legacy, beforeDeadline)
	add(programme, beforeDeadline)
	add(programme, beforeDeadline)
	add(programme, beforeDeadline)
	add(programme, time.Now())
	for range 6 {
		add(unapproved, beforeDeadline)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHeroesRoutes(r, pool)
	token := uuid.NewString()
	exec(`INSERT INTO heroes_sessions(token_hash,hero_id,expires_at)VALUES($1,$2::uuid,now()+interval '1 hour')`, heroesHash(token), hero)
	api := &adminAPI{pool: pool}
	r.PUT("/rank-policy", func(c *gin.Context) {
		c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"})
		api.saveHeroesPolicy(c)
	})
	request := func(method, path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	setMinimum := func(minimum int) {
		t.Helper()
		var version int64
		if err = pool.QueryRow(ctx, `SELECT max(version) FROM heroes_policies`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		out := request("PUT", "/rank-policy", heroesPolicy{Version: version, TripTarget: 70, RankMinimumTrips: minimum, Ranks: map[string]int{"E": 0, "D": 1, "C": 2, "B": 3, "A": 4}})
		if out["data"].(map[string]any)["rankMinimumTrips"] != float64(minimum) {
			t.Fatal("rank setting not persisted", out)
		}
	}
	check := func(minimum, count int, rank string) {
		t.Helper()
		setMinimum(minimum)
		me := request("GET", "/heroes/me", nil)["data"].(map[string]any)
		if me["rank"] != rank || me["rankQualifiedDrivers"] != float64(count) || me["onboarded"] != float64(2) {
			t.Fatalf("minimum=%d unexpected rank/count/onboarding: %v", minimum, me)
		}
		if me["nextRank"].(map[string]any)["remaining"] != float64(1) {
			t.Fatal("remaining count uses onboarding instead of qualifying drivers", me)
		}
		leaders := request("GET", "/heroes/leaderboard", nil)["data"].([]any)
		found := false
		for _, item := range leaders {
			leader := item.(map[string]any)
			if leader["isYou"] == true {
				found = true
				if leader["qualifiedDrivers"] != float64(count) {
					t.Fatal("leaderboard disagrees with rank", leader)
				}
			}
		}
		if (count > 0) != found {
			t.Fatal("unexpected leaderboard membership", leaders)
		}
	}
	check(0, 2, "C")
	check(2, 1, "D")
	check(4, 0, "E") // fourth programme trip missed the captured deadline
	for range 4 {
		add(programme, beforeDeadline)
	}
	check(2, 1, "D") // many trips still count as one driver
	check(0, 2, "C")
	var reward int64
	if err = pool.QueryRow(ctx, `SELECT reward_kobo FROM referral_attributions WHERE hero_id=$1::uuid`, hero).Scan(&reward); err != nil || reward != 700000 {
		t.Fatal("rank setting changed captured payout", reward, err)
	}
}
