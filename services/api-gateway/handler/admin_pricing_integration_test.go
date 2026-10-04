package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestPricingRequiresDistinctApprovalAndFreezesPublishedRate(t *testing.T) {
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
	creator, approver, zone := uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "PRICE_" + strings.ToUpper(zone[:8])
	seed, _ := strconv.ParseUint(zone[:4], 16, 16)
	seedY, _ := strconv.ParseUint(zone[4:8], 16, 16)
	x, y := 60+float64(seed)/100000, 10+float64(seedY)/100000
	boundary := fmt.Sprintf("MULTIPOLYGON(((%f %f,%f %f,%f %f,%f %f,%f %f)))", x, y, x+0.001, y, x+0.001, y+0.001, x, y+0.001, x, y)
	for _, id := range []string{creator, approver} {
		if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, id, "price-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)
		VALUES($1::UUID,'market',$2,$2,1,ST_GeomFromText($5,4326),'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, zone, market, creator, approver, boundary); err != nil {
		t.Fatal(err)
	}
	api := &adminAPI{pool: pool}
	body := map[string]any{"marketCode": market, "effectiveFrom": time.Now().Add(-time.Minute), "rates": []pricingRateInput{
		{PackageSlug: "bike", BaseKobo: 100, PerKmKobo: 200, PerMinuteKobo: 60},
		{PackageSlug: "auto", BaseKobo: 200, PerKmKobo: 300, PerMinuteKobo: 60},
		{PackageSlug: "sedan", BaseKobo: 300, PerKmKobo: 400, PerMinuteKobo: 60},
		{PackageSlug: "suv", BaseKobo: 400, PerKmKobo: 500, PerMinuteKobo: 60},
	}}
	raw, _ := json.Marshal(body)
	create := callAdminHandler(t, creator, api.createPricing, raw)
	if create.Code != http.StatusCreated {
		t.Fatalf("draft status=%d body=%s", create.Code, create.Body.String())
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(create.Body.Bytes(), &result)
	id := result.Data.ID
	pricingModule:=moduleApprovalConfigs[1]
	pending:=callAdminHandler(t,approver,func(c *gin.Context){api.pendingModuleApprovals(c,pricingModule)},nil)
	if pending.Code!=http.StatusOK||!strings.Contains(pending.Body.String(),id)||!strings.Contains(pending.Body.String(),creator) { t.Fatalf("pending pricing queue status=%d body=%s",pending.Code,pending.Body.String()) }
	approve := func(actor string) int {
		rec := callAdminHandler(t, actor, func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: id}}; api.approvePricing(c) }, nil)
		return rec.Code
	}
	if got := approve(creator); got != http.StatusConflict {
		t.Fatalf("self approval status=%d", got)
	}
	if got := approve(approver); got != http.StatusOK {
		t.Fatalf("second approval status=%d", got)
	}
	receipt:=callAdminHandler(t,approver,func(c *gin.Context){c.Params=gin.Params{{Key:"id",Value:id}};api.moduleDecisionReceipt(c,pricingModule)},nil)
	if receipt.Code!=http.StatusOK||!strings.Contains(receipt.Body.String(),`"status":"approved"`)||!strings.Contains(receipt.Body.String(),approver) { t.Fatalf("decision receipt status=%d body=%s",receipt.Code,receipt.Body.String()) }
	var changed int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM pricing_package_rates WHERE pricing_id=$1::UUID`, id).Scan(&changed); err != nil || changed != 4 {
		t.Fatalf("rates=%d err=%v", changed, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE pricing_package_rates SET base_kobo=999 WHERE pricing_id=$1::UUID AND package_slug='sedan'`, id); err == nil {
		t.Fatal("published rate was mutable")
	}
	for _, cfg := range moduleApprovalConfigs {
		c := callAdminHandler(t, approver, func(c *gin.Context) { api.pendingModuleApprovals(c, cfg) }, nil)
		if c.Code != http.StatusOK {
			t.Fatalf("%s queue status=%d body=%s", cfg.path, c.Code, c.Body.String())
		}
	}
	conflicts := callAdminHandler(t, approver, func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: zone}}; api.geofenceConflicts(c) }, nil)
	if conflicts.Code != http.StatusOK || !strings.Contains(conflicts.Body.String(), `"conflicts":[]`) {
		t.Fatalf("conflict preview status=%d body=%s", conflicts.Code, conflicts.Body.String())
	}
	previewBody, _ := json.Marshal(map[string]any{"kind": "market", "effectiveFrom": time.Now().Add(time.Minute), "boundary": map[string]any{"type": "MultiPolygon", "coordinates": [][][][]float64{{{{x, y}, {x + 0.001, y}, {x + 0.001, y + 0.001}, {x, y + 0.001}, {x, y}}}}}})
	preview := callAdminHandler(t, approver, api.previewGeofence, previewBody)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), zone) || !strings.Contains(preview.Body.String(), `"approvable":false`) {
		t.Fatalf("unsaved preview status=%d body=%s", preview.Code, preview.Body.String())
	}
}
