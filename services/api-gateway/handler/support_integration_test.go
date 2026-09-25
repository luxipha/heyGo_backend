package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type supportTestUsers struct{ id, role string }

func (s supportTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{s.role}}, nil
}
func (supportTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestSupportCaseUserAndStaffWorkflow(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	userID, adminID := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, userID, "support-test:"+userID, "support-human:"+userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'unused')`, adminID, "support-test-"+adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM support_cases WHERE opened_by=$1::UUID`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_users WHERE id=$1::UUID`, adminID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::UUID`, userID)
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	authenticated := router.Group("/", gatewayauth.NewMiddleware(contractTestVerifier{}, supportTestUsers{id: userID, role: "driver"}).Authenticate)
	registerSupportRoutes(authenticated, pool, nil)
	staff := router.Group("/staff", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: adminID, Username: "support"}); c.Next() })
	registerAdminSupportRoutes(staff, pool, nil, func(c *gin.Context) { c.Next() })
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer test")
		}
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	create := call(http.MethodPost, "/support/tickets", "yes", `{"topicCode":"general-other","subject":"App help","description":"Please help","clientMessageId":"`+uuid.NewString()+`"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create case: %d %s", create.Code, create.Body.String())
	}
	var created struct {
		Data struct {
			CaseID string `json:"caseId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	caseID := created.Data.CaseID
	if _, err := uuid.Parse(caseID); err != nil {
		t.Fatalf("case id=%q", caseID)
	}
	userMessageID := uuid.NewString()
	reply := `{"body":"More details","clientMessageId":"` + userMessageID + `"}`
	first := call(http.MethodPost, "/support/cases/"+caseID+"/messages", "yes", reply)
	if first.Code != http.StatusCreated {
		t.Fatalf("user message: %d %s", first.Code, first.Body.String())
	}
	second := call(http.MethodPost, "/support/cases/"+caseID+"/messages", "yes", reply)
	if second.Code != http.StatusCreated {
		t.Fatalf("idempotent user message: %d %s", second.Code, second.Body.String())
	}
	var userCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_case_messages WHERE case_id=$1::UUID AND sender_kind='user'`, caseID).Scan(&userCount); err != nil || userCount != 2 {
		t.Fatalf("messages after retry=%d err=%v", userCount, err)
	}
	staffReply := call(http.MethodPost, "/staff/support/cases/"+caseID+"/messages", "", `{"body":"We received your request","clientMessageId":"`+uuid.NewString()+`"}`)
	if staffReply.Code != http.StatusCreated {
		t.Fatalf("staff reply: %d %s", staffReply.Code, staffReply.Body.String())
	}
	messagePage := call(http.MethodGet, "/support/cases/"+caseID+"/messages?limit=1", "yes", "")
	if messagePage.Code != http.StatusOK || !strings.Contains(messagePage.Body.String(), `"nextCursor":"`) || !strings.Contains(messagePage.Body.String(), "We received your request") {
		t.Fatalf("paginated messages: %d %s", messagePage.Code, messagePage.Body.String())
	}
	update := call(http.MethodPatch, "/staff/support/cases/"+caseID, "", `{"status":"in_progress","assignedAdminId":"`+adminID+`"}`)
	if update.Code != http.StatusOK {
		t.Fatalf("update case: %d %s", update.Code, update.Body.String())
	}
	detail := call(http.MethodGet, "/support/cases/"+caseID, "yes", "")
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "We received your request") {
		t.Fatalf("case detail: %d %s", detail.Code, detail.Body.String())
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT status FROM support_cases WHERE id=$1::UUID`, caseID).Scan(&state); err != nil || state != "in_progress" {
		t.Fatalf("case status=%q err=%v", state, err)
	}
	rows, err := pool.Query(ctx, `SELECT action FROM admin_audit WHERE admin_id=$1::UUID AND action IN ('support.case.replied','support.case.updated')`, adminID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actions := 0
	for rows.Next() {
		actions++
	}
	if actions < 2 {
		t.Fatalf("expected audited staff reply and case update, got %d", actions)
	}
}
