package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	paymentservice "github.com/luxipha/heyGo_backend/services/payment-service/service"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/referrals"
)

func TestHeroStagesQualificationPaymentsAndProviderReconciliation(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
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
	hero, other, driver, rider, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO heroes_accounts(id,email,name,referral_code)VALUES($1::uuid,$1||'@example.test','Stage Hero','STAGE-'||upper(substr($1::text,1,8))),($2::uuid,$2||'@example.test','Other Hero','OTHER-'||upper(substr($2::text,1,8)))`, hero, other)
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	exec(`INSERT INTO users(id,roles,phone_e164,phone_verified_at)VALUES($1::uuid,'{}',$3,now()),($2::uuid,ARRAY['rider'],NULL,NULL)`, driver, rider, fmt.Sprintf("+23480%08d", uint64(time.Now().UnixNano())%100000000))
	exec(`INSERT INTO driver_profiles(driver_id,display_name,manual_verification_status,admin_status,admin_reviewed_at)VALUES($1::uuid,'Stage Driver','approved','approved',now()+interval '1 hour')`, driver)
	exec(`INSERT INTO driver_vehicles(driver_id,package_slug,review_status,approval_status,approved_at)VALUES($1::uuid,'sedan','approved','approved',now()+interval '1 hour')`, driver)
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key,expires_at)VALUES($1::uuid,$2::uuid,$3,'approved','test',now()+interval '5 days')`, uuid.NewString(), driver, kind)
	}
	exec(`INSERT INTO driver_inspections(id,driver_id,status)VALUES($1::uuid,$2::uuid,'completed')`, uuid.NewString(), driver)
	settings, err := referrals.Read(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	settings.Enabled = true
	deadline := 1
	settings.Rules["hero_driver"] = referrals.Rule{TripTarget: 3, RewardKobo: 3200000, DeadlineDays: &deadline, Stages: []referrals.Stage{{TripTarget: 1, RewardKobo: 700000, ApprovalMode: "automatic"}, {TripTarget: 2, RewardKobo: 1000000, ApprovalMode: "manual"}, {TripTarget: 3, RewardKobo: 1500000, ApprovalMode: "automatic", PayoutMode: "monnify"}}}
	tx, _ := pool.Begin(ctx)
	defer tx.Rollback(ctx)
	settings, err = referrals.Save(ctx, tx, settings, staff)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	if err = referrals.Claim(ctx, tx, driver, "STAGE-"+strings.ToUpper(hero[:8]), "driver"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET roles=ARRAY['driver'] WHERE id=$1::uuid`, driver)
	stages, err := referrals.ListStages(ctx, pool, hero, 0)
	if err != nil || len(stages) != 3 {
		t.Fatalf("stage snapshot: %v %v", stages, err)
	}
	// Updating live policy cannot rewrite a driver's captured stage amounts/modes.
	changed := settings.Rules["hero_driver"]
	changed.Stages[0].RewardKobo++
	changed.RewardKobo++
	settings.Rules["hero_driver"] = changed
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	if _, err = referrals.Save(ctx, tx, settings, staff); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var captured int64
	if err = pool.QueryRow(ctx, `SELECT reward_kobo FROM hero_referral_stages WHERE id=$1::uuid`, stages[0].ID).Scan(&captured); err != nil || captured != 700000 {
		t.Fatal("snapshot changed", captured, err)
	}
	fare := uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	addTrip := func(status string, old, late bool) {
		t.Helper()
		created, completed := time.Now(), time.Now()
		if old {
			created = created.Add(-24 * time.Hour)
		}
		if late {
			completed = completed.Add(48 * time.Hour)
		}
		exec(`INSERT INTO trips(id,rider_id,assigned_driver_id,ride_fare_id,status,created_at,completed_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7)`, uuid.NewString(), rider, driver, fare, status, created, completed)
	}
	addTrip("completed", true, false)
	addTrip("cancelled", false, false)
	addTrip("completed", false, true)
	if err = referrals.ProcessRewards(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM hero_referral_stages WHERE referral_id=$1::uuid AND qualified_at IS NOT NULL`, stages[0].ReferralID).Scan(&count); err != nil || count != 0 {
		t.Fatal("excluded trips qualified a stage", count, err)
	}
	addTrip("completed", false, false)
	if err = referrals.ProcessRewards(ctx, pool); err != nil {
		t.Fatal(err)
	}
	read := func() []referrals.StageProgress {
		t.Helper()
		s, e := referrals.ListStages(ctx, pool, hero, 0)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	stages = read()
	if stages[0].CreditedAt == nil || stages[1].QualifiedAt != nil || stages[0].CompletedTrips != 1 {
		t.Fatal("first automatic stage state", stages)
	}
	addTrip("completed", false, false)
	addTrip("completed", false, false)
	if err = referrals.ProcessRewards(ctx, pool); err != nil {
		t.Fatal(err)
	}
	stages = read()
	if stages[1].QualifiedAt == nil || stages[1].CreditedAt != nil || stages[2].CreditedAt == nil {
		t.Fatal("mixed approval modes failed", stages)
	}
	var wg sync.WaitGroup
	fail := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, e := pool.Begin(ctx)
			if e != nil {
				fail <- e
				return
			}
			defer tx.Rollback(ctx)
			if e = referrals.ApproveStages(ctx, tx, []string{stages[1].ID}, staff); e == nil {
				e = tx.Commit(ctx)
			}
			fail <- e
		}()
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	var earned int64
	if err = pool.QueryRow(ctx, `SELECT sum(amount_kobo) FROM heroes_wallet_entries WHERE hero_id=$1::uuid`, hero).Scan(&earned); err != nil || earned != 3200000 {
		t.Fatal("duplicate/missing stage credits", earned, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE hero_referral_stages SET reward_kobo=1 WHERE id=$1::uuid`, stages[0].ID); err == nil {
		t.Fatal("stage snapshot mutable")
	}
	key := []byte("01234567890123456789012345678901")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := registerHeroesRoutes(r, pool)
	api.email.settingsKey = key
	token := uuid.NewString()
	exec(`INSERT INTO heroes_sessions(token_hash,hero_id,expires_at)VALUES($1,$2::uuid,now()+interval '1 hour')`, heroesHash(token), hero)
	admin := &adminAPI{pool: pool, settingsKey: key}
	g := r.Group("/test", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}) })
	g.POST("/accounts/:id/payments", admin.recordHeroPayment)
	g.POST("/payments/:id/reverse", admin.reverseHeroPayment)
	g.POST("/payouts/:id/cancel", admin.cancelQueuedHeroPayout)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	require := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("want %d got %d: %s", want, w.Code, w.Body.String())
		}
	}
	// Heroes sees a deadline-consistent trip count and its own stages only.
	w := call("GET", "/heroes/drivers", nil)
	require(w, 200)
	if !strings.Contains(w.Body.String(), `"trips":3`) {
		t.Fatal("Hero trip count ignores deadline", w.Body.String())
	}
	w = call("GET", "/heroes/stages", nil)
	require(w, 200)
	if strings.Contains(w.Body.String(), staff) || !strings.Contains(w.Body.String(), stages[1].ID) {
		t.Fatal("stage privacy/data", w.Body.String())
	}
	var initiations atomic.Int32
	var providerState atomic.Value
	providerState.Store("PENDING_AUTHORIZATION")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Token") != "hero-test" {
			t.Error("missing internal authentication")
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/refund-bank-accounts/verify" {
			_ = json.NewEncoder(w).Encode(gin.H{"data": gin.H{"accountName": "Stage Hero Verified", "bankName": "Test Bank"}})
			return
		}
		switch b["action"] {
		case "ready":
			_ = json.NewEncoder(w).Encode(gin.H{"data": gin.H{"ready": true}})
		case "initiate":
			initiations.Add(1)
			w.WriteHeader(503) // response lost after provider accepted
		case "status":
			var ref string
			var amount int64
			_ = pool.QueryRow(ctx, `SELECT reference,amount_kobo FROM hero_stage_payouts WHERE id=$1::uuid`, b["id"]).Scan(&ref, &amount)
			_ = json.NewEncoder(w).Encode(gin.H{"data": gin.H{"reference": ref, "amount": json.Number(fmt.Sprintf("%d.00", amount/100)), "currency": "NGN", "destinationBankCode": "044", "destinationAccountNumber": "0123456789", "transactionReference": "MNFY-stage", "status": providerState.Load().(string)}})
		default:
			t.Errorf("unexpected provider action %s", b["action"])
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	t.Setenv("PAYMENT_SERVICE_URL", server.URL)
	t.Setenv("PAYMENT_SERVICE_AUDIENCE", "")
	t.Setenv("INTERNAL_SERVICE_TOKEN", "hero-test")
	t.Setenv("HEYGO_ADMIN_SETTINGS_KEY", hex.EncodeToString(key))
	require(call("POST", "/heroes/bank-account", gin.H{"bankName": "Test Bank", "bankCode": "044", "accountNumber": "0123456789"}), 200)
	var bank string
	if err = pool.QueryRow(ctx, `SELECT payout_account_id::text FROM heroes_accounts WHERE id=$1::uuid`, hero).Scan(&bank); err != nil {
		t.Fatal(err)
	}
	paymentID := uuid.NewString()
	body := gin.H{"id": paymentID, "bankAccountId": bank, "amountKobo": 700000, "reference": "manual-stage-" + paymentID, "paidAt": time.Now(), "stageIds": []string{stages[0].ID}}
	path := "/test/accounts/" + hero + "/payments"
	require(call("POST", path, body), 200)
	require(call("POST", path, body), 200)
	stages = read()
	if stages[0].PaidAt == nil || stages[1].PaidAt != nil {
		t.Fatal("manual payment not assigned to stage")
	}
	body["id"] = uuid.NewString()
	body["reference"] = "duplicate-stage-" + uuid.NewString()
	require(call("POST", path, body), 409)
	body["stageIds"] = []string{stages[1].ID}
	body["amountKobo"] = 999999
	require(call("POST", path, body), 409)
	body["stageIds"] = []string{}
	body["amountKobo"] = 1000000
	require(call("POST", path, body), 409)
	require(call("POST", "/test/payments/"+paymentID+"/reverse", gin.H{"reason": "Incorrect transfer recorded"}), 200)
	if read()[0].PaidAt != nil {
		t.Fatal("reversed manual payment still paid")
	}
	exec(`INSERT INTO admin_monnify_settings(enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext)VALUES(true,'sandbox','test','test','test')`)
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	queued, err := referrals.QueueStagePayout(ctx, tx, stages[0].ID, &staff)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	require(call("POST", "/test/payouts/"+queued+"/cancel", gin.H{}), 200)
	body["id"] = uuid.NewString()
	body["reference"] = "manual-after-cancel-" + uuid.NewString()
	body["stageIds"] = []string{stages[0].ID}
	body["amountKobo"] = 700000
	require(call("POST", path, body), 200)
	// Automatic Monnify payout reserves stage 3 and reconciles an ambiguous submit.
	exec(`INSERT INTO admin_monnify_settings(enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext)VALUES(true,'sandbox','test','test','test')`)
	if err = RunHeroStagePayoutJobsOnce(ctx, pool); err != nil {
		t.Fatal(err)
	}
	stages = read()
	if stages[2].PayoutStatus == nil || *stages[2].PayoutStatus != "PENDING_AUTHORIZATION" || stages[2].PaidAt != nil || initiations.Load() != 1 {
		t.Fatal("unconfirmed transfer marked paid", stages[2], initiations.Load())
	}
	body["stageIds"] = []string{stages[2].ID}
	body["amountKobo"] = 1500000
	require(call("POST", path, body), 409)
	exec(`UPDATE hero_stage_payouts SET updated_at=now()-interval '2 minutes' WHERE stage_id=$1::uuid`, stages[2].ID)
	providerState.Store("SUCCESS")
	if err = RunHeroStagePayoutJobsOnce(ctx, pool); err != nil {
		t.Fatal(err)
	}
	stages = read()
	if stages[2].PaidAt == nil || initiations.Load() != 1 {
		t.Fatal("confirmed transfer missing or initiated twice", stages[2])
	}
	// A mismatched terminal query cannot settle/rewrite another payment.
	var result paymentservice.TransferResult
	result.Reference = "wrong"
	result.Amount = "15000.00"
	result.Currency = "NGN"
	result.Status = "SUCCESS"
	result.DestinationBankCode = "044"
	result.DestinationAccountNumber = "0123456789"
	if err = applyHeroTransferStatus(ctx, pool, *stages[2].PayoutID, result, key); err == nil {
		t.Fatal("mismatched provider proof accepted")
	}
	require(call("POST", "/test/payments/"+*stages[2].PayoutID+"/reverse", gin.H{"reason": "Cannot manually reverse Monnify"}), 404)
	exec(`UPDATE hero_stage_payouts SET updated_at=now()-interval '2 minutes' WHERE stage_id=$1::uuid`, stages[2].ID)
	providerState.Store("REVERSED")
	if err = RunHeroStagePayoutJobsOnce(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if read()[2].PaidAt != nil {
		t.Fatal("provider reversal still shows paid")
	}
	if _, err = pool.Exec(ctx, `UPDATE heroes_wallet_entries SET amount_kobo=0 WHERE hero_id=$1::uuid`, hero); err == nil {
		t.Fatal("stage credits editable")
	}
	// Verify legacy approval cannot award a staged parent a second time.
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	err = referrals.Approve(ctx, tx, []string{stages[0].ReferralID}, staff)
	tx.Rollback(ctx)
	if err == nil {
		t.Fatal("staged parent approved as a single reward")
	}
	if adminRoutePermission("POST", "/admin/referrals/stages/payout") != permissionFinanceWrite || adminRoutePermission("GET", "/admin/referrals/stages") != permissionFinanceRead {
		t.Fatal("stage permissions incorrect")
	}
}
