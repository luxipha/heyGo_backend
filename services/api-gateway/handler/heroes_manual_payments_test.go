package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHeroesManualPaymentsBalancesPrivacyAndConcurrency(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable database required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 6})
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
	hero, other, driver, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO heroes_accounts(id,email,name,referral_code)VALUES($1::uuid,$1||'@example.test','Ada',$1),($2::uuid,$2||'@example.test','Other',$2)`, hero, other)
	exec(`INSERT INTO heroes_sessions(token_hash,hero_id,expires_at)VALUES($1,$2::uuid,now()+interval '1 hour'),($3,$4::uuid,now()+interval '1 hour')`, heroesHash("manual-hero"), hero, heroesHash("manual-other"), other)
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles)VALUES($1::uuid,$1,$1,ARRAY['driver'])`, driver)
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	ref := uuid.NewString()
	exec(`INSERT INTO heroes_referrals(id,hero_id,driver_id,policy_id)SELECT $1::uuid,$2::uuid,$3::uuid,id FROM heroes_policies ORDER BY version DESC LIMIT 1`, ref, hero, driver)
	exec(`INSERT INTO heroes_wallet_entries(id,hero_id,referral_id,amount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,100000)`, uuid.NewString(), hero, ref)
	r := gin.New()
	a := registerHeroesRoutes(r, pool)
	key := []byte("01234567890123456789012345678901")
	a.email.settingsKey = key
	admin := &adminAPI{pool: pool, settingsKey: key}
	g := r.Group("/test")
	g.Use(func(c *gin.Context) {
		role := "super_admin"
		if c.GetHeader("X-Read-Only") == "true" {
			role = "operations"
		}
		c.Set("admin", adminIdentity{ID: staff, Role: role})
		c.Next()
	})
	g.GET("/accounts/:id", admin.heroesPaymentDetail)
	g.GET("/payments", admin.heroesPaymentsList)
	g.POST("/accounts/:id/payments", admin.recordHeroPayment)
	g.POST("/payments/:id/reverse", admin.reverseHeroPayment)
	call := func(method, path, token string, body any, readOnly bool) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		q := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", "Bearer "+token)
		if readOnly {
			q.Header.Set("X-Read-Only", "true")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	require := func(want, got int, out any) {
		t.Helper()
		if want != got {
			t.Fatalf("want %d got %d: %v", want, got, out)
		}
	}
	c, o := call("POST", "/heroes/bank-account", "manual-hero", gin.H{"bankName": "Test Bank", "accountName": "Ada Hero", "accountNumber": "0123456789"}, false)
	require(200, c, o)
	c, o = call("GET", "/heroes/wallet", "manual-hero", nil, false)
	require(200, c, o)
	if strings.Contains(string(mustJSON(o)), "0123456789") {
		t.Fatal("Full account leaked to Hero read response")
	}
	bank := o["data"].(map[string]any)["bankAccount"].(map[string]any)
	bankID := bank["id"].(string)
	c, o = call("GET", "/heroes/wallet", "manual-other", nil, false)
	require(200, c, o)
	if o["data"].(map[string]any)["bankAccount"] != nil {
		t.Fatal("Other Hero can read bank details")
	}
	c, o = call("GET", "/test/accounts/"+hero, "", nil, true)
	require(200, c, o)
	if strings.Contains(string(mustJSON(o)), "0123456789") {
		t.Fatal("Read-only staff received full bank account")
	}
	c, o = call("GET", "/test/accounts/"+hero, "", nil, false)
	require(200, c, o)
	if !strings.Contains(string(mustJSON(o)), "0123456789") {
		t.Fatal("Payment manager cannot see account")
	}
	payment := func(amount int64) gin.H {
		return gin.H{"id": uuid.NewString(), "amountKobo": amount, "reference": "bank-" + uuid.NewString(), "bankAccountId": bankID, "paidAt": time.Now().UTC().Truncate(time.Millisecond)}
	}
	first := payment(40000)
	path := "/test/accounts/" + hero + "/payments"
	c, o = call("POST", path, "", first, false)
	require(200, c, o)
	c, o = call("POST", path, "", first, false)
	require(200, c, o)
	duplicate := payment(1000)
	duplicate["reference"] = first["reference"]
	c, o = call("POST", path, "", duplicate, false)
	require(409, c, o)
	c, o = call("POST", path, "", payment(60001), false)
	require(409, c, o)
	if _, e := pool.Exec(ctx, `UPDATE heroes_manual_payments SET amount_kobo=1 WHERE hero_id=$1::uuid`, hero); e == nil {
		t.Fatal("Payment record was mutable")
	}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); status, _ := call("POST", path, "", payment(40000), false); results <- status }()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for status := range results {
		if status == 200 {
			ok++
		}
		if status == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("Concurrent overpayment: %d success %d conflict", ok, conflict)
	}
	c, o = call("GET", "/heroes/wallet", "manual-hero", nil, false)
	require(200, c, o)
	data := o["data"].(map[string]any)
	if data["earnedKobo"] != float64(100000) || data["paidKobo"] != float64(80000) || data["balanceKobo"] != float64(20000) {
		t.Fatal("Wrong balances", data)
	}
	reverse := "/test/payments/" + first["id"].(string) + "/reverse"
	for i := 0; i < 2; i++ {
		c, o = call("POST", reverse, "", gin.H{"reason": "Wrong transfer record"}, false)
		require(200, c, o)
	}
	c, o = call("GET", "/heroes/wallet", "manual-hero", nil, false)
	require(200, c, o)
	data = o["data"].(map[string]any)
	if data["paidKobo"] != float64(40000) || data["balanceKobo"] != float64(60000) {
		t.Fatal("Correction duplicated or lost", data)
	}
	// A corrected transfer can be re-recorded with its actual reference and amount.
	corrected := payment(10000)
	corrected["reference"] = first["reference"]
	c, o = call("POST", path, "", corrected, false)
	require(200, c, o)
	c, o = call("GET", "/heroes/wallet", "manual-hero", nil, false)
	require(200, c, o)
	data = o["data"].(map[string]any)
	if data["paidKobo"] != float64(50000) || data["balanceKobo"] != float64(50000) {
		t.Fatal("Corrected replacement accounting failed", data)
	}
	c, o = call("POST", "/heroes/bank-account", "manual-hero", gin.H{"bankName": "New Bank", "accountName": "Ada Hero", "accountNumber": "9876543210"}, false)
	require(200, c, o)
	c, o = call("POST", path, "", payment(1000), false)
	require(409, c, o)
	c, o = call("GET", "/heroes/wallet", "manual-hero", nil, false)
	require(200, c, o)
	data = o["data"].(map[string]any)
	if !strings.Contains(string(mustJSON(data["payments"])), "******6789") {
		t.Fatal("Payment lost original destination snapshot")
	}
	c, o = call("GET", "/test/payments", "", nil, false)
	require(200, c, o)
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
