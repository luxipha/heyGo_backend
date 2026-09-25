package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestStatutoryRuleInputNormalizesSupportedRule(t *testing.T) {
	amount := int64(200000)
	input := statutoryRuleInput{LedgerCode: "test_levy", Name: "Example", Authority: "Authority", Jurisdiction: "Lagos", CalculationType: "percentage", CalculationBase: "trip_fare", CalculationConfig: json.RawMessage(`{"rateBps":750}`), RegionCode: stringPtr("lagos"), MarketCode: stringPtr("lagos"), Bearer: "driver", FundingSource: "operating_balance", MaximumKobo: &amount, EffectiveFrom: time.Now().Add(time.Hour)}
	if err := input.normalize(); err != nil {
		t.Fatal(err)
	}
	if input.LedgerCode != "TEST_LEVY" || input.RegionMatch != "either" || *input.MarketCode != "LAGOS" {
		t.Fatalf("unexpected normalization: %#v", input)
	}
}
func TestStatutoryRuleInputRejectsUnsafeOrInconsistentRules(t *testing.T) {
	base := statutoryRuleInput{LedgerCode: "TEST_LEVY", Name: "Example", Authority: "Authority", Jurisdiction: "NG", CalculationType: "formula", CalculationBase: "trip_fare", CalculationConfig: json.RawMessage(`{"op":"exec","args":[{"constKobo":1},{"constKobo":2}]}`), Bearer: "driver", FundingSource: "operating_balance", EffectiveFrom: time.Now().Add(time.Hour)}
	if err := base.normalize(); err == nil {
		t.Fatal("accepted unsupported formula operator")
	}
	base.CalculationType = "fixed"
	base.CalculationConfig = json.RawMessage(`{"amountNaira":250}`)
	base.FundingSource = "platform"
	if err := base.normalize(); err == nil {
		t.Fatal("accepted funding source inconsistent with charge bearer")
	}
}
func stringPtr(v string) *string { return &v }

func TestAdminStatutoryRuleTwoPersonApprovalOverlapAndRetirement(t *testing.T) {
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
	suffix := uuid.NewString()
	first, second := uuid.NewString(), uuid.NewString()
	for i, id := range []string{first, second} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, id, "stat-rule-"+suffix+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	api := &adminAPI{pool: pool}
	base := statutoryRuleInput{LedgerCode: "TEST_" + suffix[:8], Name: "Test launch rule", Authority: "Test authority", Jurisdiction: "Lagos", CalculationType: "fixed", CalculationBase: "trip_fare", CalculationConfig: json.RawMessage(`{"amountNaira":25}`), MarketCode: stringPtr("LAGOS"), Bearer: "driver", FundingSource: "operating_balance", EffectiveFrom: time.Now().Add(time.Hour)}
	create := func(actor string, input statutoryRuleInput) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(input)
		rec := callAdminHandler(t, actor, func(c *gin.Context) { api.createStatutoryRule(c) }, raw)
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Data.ID
	}
	status, id := create(first, base)
	if status != http.StatusCreated || id == "" {
		t.Fatalf("create status=%d ruleID=%s", status, id)
	}
	approve := func(actor, ruleID string) *httptest.ResponseRecorder {
		return callAdminHandler(t, actor, func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: ruleID}}; api.approveStatutoryRule(c) }, nil)
	}
	if got := approve(first, id); got.Code != http.StatusConflict {
		t.Fatalf("creator approved own rule: status=%d body=%s", got.Code, got.Body.String())
	}
	if got := approve(second, id); got.Code != http.StatusOK {
		t.Fatalf("second staff approval: status=%d body=%s", got.Code, got.Body.String())
	}
	overlap := base
	overlap.EffectiveFrom = base.EffectiveFrom.Add(2 * time.Hour)
	status, overlapID := create(first, overlap)
	if status != http.StatusCreated {
		t.Fatalf("overlap draft create status=%d", status)
	}
	if got := approve(second, overlapID); got.Code != http.StatusConflict {
		t.Fatalf("overlapping rule approved: status=%d body=%s", got.Code, got.Body.String())
	}
	nonDriver := base
	nonDriver.LedgerCode = "RIDER_" + suffix[:8]
	nonDriver.Bearer = "rider"
	nonDriver.FundingSource = "rider_payment"
	status, riderID := create(first, nonDriver)
	if status != http.StatusCreated {
		t.Fatalf("non-driver draft create status=%d", status)
	}
	if got := approve(second, riderID); got.Code != http.StatusConflict {
		t.Fatalf("unsupported rider funding approved: status=%d body=%s", got.Code, got.Body.String())
	}
	retirementBody, _ := json.Marshal(map[string]time.Time{"effectiveUntil": time.Now().Add(48 * time.Hour)})
	request := callAdminHandler(t, first, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: id}}
		api.requestStatutoryRuleRetirement(c)
	}, retirementBody)
	var retirement struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(request.Body.Bytes(), &retirement)
	if request.Code != http.StatusCreated || retirement.Data.ID == "" {
		t.Fatalf("retirement request status=%d body=%s", request.Code, request.Body.String())
	}
	approved := callAdminHandler(t, second, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: id}, {Key: "requestID", Value: retirement.Data.ID}}
		api.approveStatutoryRuleRetirement(c)
	}, nil)
	if approved.Code != http.StatusOK {
		t.Fatalf("retirement approval status=%d body=%s", approved.Code, approved.Body.String())
	}
}

func callAdminHandler(t *testing.T, adminID string, handler gin.HandlerFunc, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/admin/test", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	ctx.Set("admin", adminIdentity{ID: adminID, Username: "staff"})
	handler(ctx)
	return recorder
}
