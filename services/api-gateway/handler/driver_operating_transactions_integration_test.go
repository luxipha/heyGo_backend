package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type operatingHistoryTestUsers struct{ id string }

func (s operatingHistoryTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"driver"}}, nil
}
func (operatingHistoryTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestDriverOperatingBalanceTransactionHistoryPaginationAndOwnership(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ownerID, otherID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{ownerID, otherID} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "operating-history-"+id, "operating-history-"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,0)`, id); err != nil {
			t.Fatal(err)
		}
	}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	entries := []struct {
		id, driverID string
		delta        int64
	}{
		{uuid.NewString(), ownerID, 500_000},
		{uuid.NewString(), ownerID, -25_000},
		{uuid.NewString(), ownerID, 125_000},
		{uuid.NewString(), otherID, 9_999_999},
	}
	ownerIDs := []string{entries[0].id, entries[1].id, entries[2].id}
	sort.Sort(sort.Reverse(sort.StringSlice(ownerIDs)))
	expectedDeltaByID := map[string]int64{}
	var balance int64
	for _, entry := range entries {
		expectedDeltaByID[entry.id] = entry.delta
		balance += entry.delta
		if entry.driverID == otherID {
			balance = entry.delta
		}
		if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details,created_at)
			VALUES($1::UUID,$2::UUID,$3,$4,$5,$6,'{"source":"test"}'::JSONB,$7)`, entry.id, entry.driverID, operatingEntryKind(entry.delta), entry.delta, balance, "history-test:"+entry.id, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	users := operatingHistoryTestUsers{id: ownerID}
	router := gin.New()
	authenticated := router.Group("/", gatewayauth.NewMiddleware(contractTestVerifier{}, users).Authenticate)
	driver := authenticated.Group("/driver", gatewayauth.RequireRole("driver"))
	api := &driverAPI{pool: pool}
	driver.GET("/operating-balance/transactions", api.operatingBalanceTransactions)
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	first := request("/driver/operating-balance/transactions?limit=2")
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage struct {
		Data []operatingBalanceTransaction `json:"data"`
		Meta struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Data) != 2 || firstPage.Meta.NextCursor == nil {
		t.Fatalf("first page count=%d cursor=%v", len(firstPage.Data), firstPage.Meta.NextCursor)
	}
	if firstPage.Data[0].ID != ownerIDs[0] || firstPage.Data[1].ID != ownerIDs[1] {
		t.Fatalf("first page order=%s,%s", firstPage.Data[0].ID, firstPage.Data[1].ID)
	}
	second := request("/driver/operating-balance/transactions?limit=2&cursor=" + *firstPage.Meta.NextCursor)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPage struct {
		Data []operatingBalanceTransaction `json:"data"`
		Meta struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Data) != 1 || secondPage.Data[0].ID != ownerIDs[2] || secondPage.Meta.NextCursor != nil {
		t.Fatalf("second page=%+v cursor=%v", secondPage.Data, secondPage.Meta.NextCursor)
	}
	if secondPage.Data[0].DeltaKobo != expectedDeltaByID[secondPage.Data[0].ID] || secondPage.Data[0].Details["source"] != "test" {
		t.Fatalf("entry payload=%+v", secondPage.Data[0])
	}

	if response := request("/driver/operating-balance/transactions?cursor=bad"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("/driver/operating-balance/transactions?limit=101"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d body=%s", response.Code, response.Body.String())
	}
	otherRouter := gin.New()
	otherGroup := otherRouter.Group("/", gatewayauth.NewMiddleware(contractTestVerifier{}, operatingHistoryTestUsers{id: otherID}).Authenticate)
	otherDriver := otherGroup.Group("/driver", gatewayauth.RequireRole("driver"))
	otherDriver.GET("/operating-balance/transactions", api.operatingBalanceTransactions)
	otherRequest := httptest.NewRequest(http.MethodGet, "/driver/operating-balance/transactions?limit=10", nil)
	otherRequest.Header.Set("Authorization", "Bearer test")
	otherResponse := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherResponse, otherRequest)
	var otherPage struct {
		Data []operatingBalanceTransaction `json:"data"`
	}
	if err := json.Unmarshal(otherResponse.Body.Bytes(), &otherPage); err != nil {
		t.Fatal(err)
	}
	if len(otherPage.Data) != 1 || otherPage.Data[0].ID != entries[3].id {
		t.Fatalf("other driver's history leaked or missing: %+v", otherPage.Data)
	}
}

func operatingEntryKind(delta int64) string {
	if delta > 0 {
		return "topup"
	}
	return "statutory_charge"
}
