package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

// Even empty queues exercise all SQL projections and cursor parameter types.
func TestFinanceContextQueries(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	dbctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	a := &adminAPI{pool: pool}
	for _, tc := range []struct {
		path string
		call func(*gin.Context)
	}{
		{"topups", a.financeTopups}, {"charges", a.financeCharges},
		{"arrears", a.financeArrears}, {"settlements", a.financeSettlements},
		{"debt-payments", a.financeDebtPayments}, {"payments", a.financePayments}, {"handoffs", a.financeHandoffs},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/admin/finance/"+tc.path+"?driverId="+uuid.NewString(), nil)
			tc.call(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
