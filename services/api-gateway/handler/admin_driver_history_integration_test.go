package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestDriverReviewHistoryPaginatesWithoutStorageKeys(t *testing.T) {
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
	driverID := uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "history:"+driverID, "history-human:"+driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(dbctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driverID); err != nil {
		t.Fatal(err)
	}
	docIDs := []string{uuid.NewString(), uuid.NewString()}
	inspectionIDs := []string{uuid.NewString(), uuid.NewString()}
	for i := range docIDs {
		at := time.Date(2098, 1, 1, 0, 0, i, 0, time.UTC)
		if _, err := pool.Exec(dbctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,submitted_at) VALUES($1::UUID,$2::UUID,'driver_license','pending',$3,$4)`, docIDs[i], driverID, "private-document-"+docIDs[i], at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(dbctx, `INSERT INTO driver_inspections(id,driver_id,status,report_storage_key,created_at) VALUES($1::UUID,$2::UUID,'report_received',$3,$4)`, inspectionIDs[i], driverID, "private-report-"+inspectionIDs[i], at); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = pool.Exec(ctx, `DELETE FROM driver_documents WHERE driver_id=$1::UUID`, driverID)
		_, _ = pool.Exec(ctx, `DELETE FROM driver_inspections WHERE driver_id=$1::UUID`, driverID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, driverID)
	}()
	api := &adminAPI{pool: pool}
	check := func(kind string, handler gin.HandlerFunc, firstID, secondID string) {
		t.Helper()
		cursor := ""
		for _, want := range []string{secondID, firstID} {
			path := "/admin/drivers/" + driverID + "/" + kind + "/history?limit=1"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, path, nil)
			ctx.Params = gin.Params{{Key: "driverID", Value: driverID}}
			handler(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", kind, recorder.Code, recorder.Body.String())
			}
			var response struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Meta struct {
					NextCursor *string `json:"nextCursor"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Data) != 1 || response.Data[0].ID != want {
				t.Fatalf("%s page=%s err=%v", kind, recorder.Body.String(), err)
			}
			if kind == "documents" && (containsPrivateKey(recorder.Body.String(), "private-document-") || containsPrivateKey(recorder.Body.String(), "storageKey")) {
				t.Fatalf("document storage key leaked: %s", recorder.Body.String())
			}
			if kind == "inspections" && (containsPrivateKey(recorder.Body.String(), "private-report-") || containsPrivateKey(recorder.Body.String(), "storageKey")) {
				t.Fatalf("inspection storage key leaked: %s", recorder.Body.String())
			}
			if want == secondID {
				if response.Meta.NextCursor == nil {
					t.Fatal("missing cursor")
				}
				cursor = *response.Meta.NextCursor
			}
		}
	}
	check("documents", api.driverDocumentHistory, docIDs[0], docIDs[1])
	check("inspections", api.driverInspectionHistory, inspectionIDs[0], inspectionIDs[1])
}

func containsPrivateKey(body, key string) bool { return strings.Contains(body, key) }
