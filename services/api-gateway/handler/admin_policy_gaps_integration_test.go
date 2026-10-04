package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestRefundPolicyRetirementAndNoShowPolicyDetail(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	dbctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	requester, approver := uuid.NewString(), uuid.NewString()
	for i, id := range []string{requester, approver} {
		if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, id, "policy-gap-"+suffix+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	api := &adminAPI{pool: pool}

	refundPolicyID := uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO refund_policy_versions(id,version,reason_codes,max_request_kobo,max_daily_kobo,effective_from,state,proposed_by,approved_by,approved_at)
		SELECT $1::UUID,COALESCE(MAX(version),0)+1,ARRAY['test_reason'],10000,20000,NOW()-INTERVAL '1 hour','approved',$2::UUID,$3::UUID,NOW() FROM refund_policy_versions`, refundPolicyID, requester, approver); err != nil {
		t.Fatal(err)
	}
	retireAt := time.Now().Add(48 * time.Hour).UTC()
	body, _ := json.Marshal(map[string]time.Time{"effectiveUntil": retireAt})
	request := callAdminHandler(t, requester, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: refundPolicyID}}
		api.requestRefundPolicyRetirement(c)
	}, body)
	var retirement struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request.Body.Bytes(), &retirement); err != nil {
		t.Fatal(err)
	}
	if request.Code != http.StatusCreated || retirement.Data.ID == "" {
		t.Fatalf("refund retirement request status=%d body=%s", request.Code, request.Body.String())
	}

	refundConfig := moduleApprovalConfigs[len(moduleApprovalConfigs)-1]
	queue := callAdminHandler(t, approver, func(c *gin.Context) { api.pendingModuleApprovals(c, refundConfig) }, nil)
	if queue.Code != http.StatusOK || !strings.Contains(queue.Body.String(), retirement.Data.ID) {
		t.Fatalf("refund retirement queue status=%d body=%s", queue.Code, queue.Body.String())
	}
	approved := callAdminHandler(t, approver, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: refundPolicyID}, {Key: "requestID", Value: retirement.Data.ID}}
		api.approveRefundPolicyRetirement(c)
	}, nil)
	if approved.Code != http.StatusOK {
		t.Fatalf("refund retirement approval status=%d body=%s", approved.Code, approved.Body.String())
	}
	receipt := callAdminHandler(t, approver, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: refundPolicyID}, {Key: "requestID", Value: retirement.Data.ID}}
		api.moduleRetirementReceipt(c, refundConfig)
	}, nil)
	if receipt.Code != http.StatusOK || !strings.Contains(receipt.Body.String(), `"status":"approved"`) {
		t.Fatalf("refund retirement receipt status=%d body=%s", receipt.Code, receipt.Body.String())
	}

	noShowPolicyID := uuid.NewString()
	market := "TEST" + strings.ToUpper(strings.ReplaceAll(suffix[:8], "-", ""))
	if _, err := pool.Exec(dbctx, `INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,created_by)
		VALUES($1::UUID,$2,1,7,25000,'draft',NOW()+INTERVAL '1 hour',$3::UUID)`, noShowPolicyID, market, requester); err != nil {
		t.Fatal(err)
	}
	detail := callAdminHandler(t, requester, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: noShowPolicyID}}
		api.noShowPolicyDetail(c)
	}, nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"waitMinutes":7`) || !strings.Contains(detail.Body.String(), `"feeKobo":25000`) {
		t.Fatalf("no-show policy detail status=%d body=%s", detail.Code, detail.Body.String())
	}
}
