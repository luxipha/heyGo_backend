package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCataloguePublicationInspectionAndDynamicPricing(t *testing.T) {
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	creator, approver, id, model, driver := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, actor := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$1,'test')`, actor)
	}
	api := &adminAPI{pool: pool}
	call := func(actor string, handler gin.HandlerFunc, body any, want int) string {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := callAdminHandler(t, actor, handler, raw)
		if r.Code != want {
			t.Fatalf("want %d got %d: %s", want, r.Code, r.Body.String())
		}
		return r.Body.String()
	}
	category := map[string]any{"id": id, "name": "Category " + id, "description": "Test", "passengers": 4, "minimumYear": 2018, "requiresAc": true, "revision": 0, "modelIds": []string{}}
	call(creator, api.saveCategory, category, 200)
	stale := map[string]any{}
	for key, value := range category {
		stale[key] = value
	}
	stale["name"] = "Changed"
	call(creator, api.saveCategory, stale, 409)
	vehicle := map[string]any{"id": model, "make": "Make " + model, "model": "Model", "variant": "V", "yearFrom": 2018, "yearTo": 2025, "bodyType": "Sedan", "categoryIds": []string{id}, "revision": 0}
	call(creator, api.saveCatalogueVehicles, map[string]any{"vehicles": []any{vehicle}}, 200)
	overlapping := map[string]any{}
	for key, value := range vehicle {
		overlapping[key] = value
	}
	overlapping["id"] = uuid.NewString()
	overlapping["yearFrom"] = 2020
	call(creator, api.saveCatalogueVehicles, map[string]any{"vehicles": []any{overlapping}}, 409)
	change := func(actor string, category bool, status string, want int) {
		t.Helper()
		table, target, handler := "vehicle_catalogue", model, api.catalogueVehicleStatus
		if category {
			table, target, handler = "ride_categories", id, api.categoryStatus
		}
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT revision FROM `+table+` WHERE id=$1::UUID`, target).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		call(actor, func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: target}}; handler(c) }, map[string]any{"status": status, "revision": revision}, want)
	}
	// A batch is atomic: a later invalid row must not leave an earlier valid model.
	batchID := uuid.NewString()
	batch := map[string]any{"id": batchID, "make": "Batch " + batchID, "model": "Model", "yearFrom": 2020, "yearTo": 2021, "bodyType": "SUV", "categoryIds": []string{}}
	call(creator, api.saveCatalogueVehicles, map[string]any{"vehicles": []any{batch, map[string]any{"id": uuid.NewString(), "make": "", "model": "Invalid"}}}, 400)
	var saved bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vehicle_catalogue WHERE id=$1::UUID)`, batchID).Scan(&saved); err != nil || saved {
		t.Fatal("partially committed import")
	}
	change(creator, false, "active", 200)
	change(creator, true, "pending", 409) // no published pricing yet
	market := "CAT_" + strings.ToUpper(id[:8])
	zone := uuid.NewString()
	exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,'market',$2,$2,1,ST_GeomFromText('MULTIPOLYGON(((100 20,101 20,101 21,100 21,100 20)))',4326),'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, zone, market, creator, approver)
	raw := call(creator, api.createPricing, map[string]any{"marketCode": market, "effectiveFrom": time.Now().Add(-time.Minute), "rates": []pricingRateInput{{PackageSlug: "ride-" + id, BaseKobo: 100, PerKmKobo: 200, PerMinuteKobo: 60}}}, 201)
	var pricing struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	if err = json.Unmarshal([]byte(raw), &pricing); err != nil {
		t.Fatal(err)
	}
	approve := func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: pricing.Data.ID}}
		api.approvePricing(c)
	}
	call(creator, approve, nil, 409)
	call(approver, approve, nil, 200)
	change(creator, true, "pending", 200)
	change(creator, true, "active", 409)
	change(approver, true, "active", 200)
	category["revision"] = 3
	call(creator, api.saveCategory, category, 409)
	change(creator, false, "draft", 409) // cannot silently edit an active category's model
	snapshot := call(creator, api.readCatalogue, nil, 200)
	if !strings.Contains(snapshot, id) || !strings.Contains(snapshot, model) {
		t.Fatal("persisted catalogue missing")
	}
	exec(`INSERT INTO users(id,roles) VALUES($1::UUID,ARRAY['driver'])`, driver)
	exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driver)
	actual := map[string]any{"catalogueModelId": model, "categoryIds": []string{id}, "year": 2022, "passengerSeats": 4, "workingAc": false, "conditionNotes": "Good condition", "plate": "TEST-1", "color": "White"}
	update := func(c *gin.Context) {
		c.Params = gin.Params{{Key: "driverID", Value: driver}}
		api.updateDriverVehicle(c)
	}
	call(creator, update, actual, 409)
	actual["workingAc"] = true
	call(creator, update, actual, 200)
	mobile := &driverAPI{pool: pool}
	ownerRead := func(handler gin.HandlerFunc) string {
		return call(creator, func(c *gin.Context) {
			c.Set("authenticated-user", gatewayauth.User{ID: driver, Roles: []string{"driver"}})
			handler(c)
		}, nil, 200)
	}
	var projection struct {
		Data struct {
			Make           string `json:"make"`
			BodyType       string `json:"bodyType"`
			PassengerSeats int    `json:"passengerSeats"`
			WorkingAC      bool   `json:"workingAc"`
			Categories     []struct {
				Name string `json:"name"`
			} `json:"categories"`
		}
	}
	if err = json.Unmarshal([]byte(ownerRead(mobile.vehicle)), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Data.Make != "Make "+model || projection.Data.BodyType != "Sedan" || projection.Data.PassengerSeats != 4 || !projection.Data.WorkingAC || len(projection.Data.Categories) != 1 || projection.Data.Categories[0].Name != "Category "+id {
		t.Fatal("Driver projection does not reflect Admin inspection")
	}
	preferences := ownerRead(mobile.ridePreferences)
	if !strings.Contains(preferences, "rideTypeLabels") || !strings.Contains(preferences, "Category "+id) {
		t.Fatal("Driver preferences are missing the Admin category name")
	}
	var eligible bool
	check := func(want bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT driver_category_eligible($1::UUID,$2)`, driver, "ride-"+id).Scan(&eligible); err != nil || eligible != want {
			t.Fatalf("eligible=%v want=%v err=%v", eligible, want, err)
		}
	}
	// One actual car can hold independently inspected membership in multiple categories.
	secondID := uuid.NewString()
	exec(`INSERT INTO ride_categories(id,slug,name,passengers,minimum_year,status) VALUES($1::UUID,$2,$2,3,2018,'active')`, secondID, "multi-"+secondID)
	exec(`INSERT INTO vehicle_category_models(category_id,model_id) VALUES($1::UUID,$2::UUID)`, secondID, model)
	exec(`INSERT INTO driver_vehicle_categories(driver_id,category_id,reviewed_by) VALUES($1::UUID,$2::UUID,$3::UUID)`, driver, secondID, creator)
	var secondEligible bool
	if err = pool.QueryRow(ctx, `SELECT driver_category_eligible($1::UUID,$2)`, driver, "multi-"+secondID).Scan(&secondEligible); err != nil || !secondEligible {
		t.Fatal("additional category unavailable")
	}
	check(true)
	inspection := uuid.NewString()
	exec(`INSERT INTO driver_inspections(id,driver_id,report_storage_key,status,vehicle_revision) VALUES($1::UUID,$2::UUID,'test','report_received',0)`, inspection, driver)
	review := func(c *gin.Context) {
		c.Params = gin.Params{{Key: "driverID", Value: driver}, {Key: "inspectionID", Value: inspection}}
		api.reviewInspection(c)
	}
	call(approver, review, map[string]any{"status": "completed", "reason": "Reviewed"}, 409)
	exec(`UPDATE driver_inspections SET vehicle_revision=1 WHERE id=$1::UUID`, inspection)
	call(approver, review, map[string]any{"status": "completed", "reason": "Reviewed"}, http.StatusOK)
	if !strings.Contains(ownerRead(mobile.inspection), `"status":"completed"`) {
		t.Fatal("current approved report was not exposed")
	}
	call(creator, update, actual, 200)
	if !strings.Contains(ownerRead(mobile.inspection), `"status":"required"`) || !strings.Contains(ownerRead(mobile.documents), `"type":"vehicle_inspection","status":"required"`) {
		t.Fatal("Driver reused an old inspection after vehicle edit")
	}
	change(creator, true, "inactive", 200)
	check(false)
	change(creator, true, "archived", 200)
	var frozen int64
	if err = pool.QueryRow(ctx, `SELECT base_kobo FROM pricing_package_rates WHERE pricing_id=$1::UUID`, pricing.Data.ID).Scan(&frozen); err != nil || frozen != 100 {
		t.Fatal("historical pricing changed")
	}
}
