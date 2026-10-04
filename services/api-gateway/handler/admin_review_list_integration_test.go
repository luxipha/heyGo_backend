package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestAdminDriverListPaginatesAfterFiltering(t *testing.T) {
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
	ids := []string{uuid.NewString(), uuid.NewString()}
	for i, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "admin-list:"+id, "admin-list-human:"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name,admin_status,updated_at) VALUES($1::UUID,$2,'rejected',$3)`, id, "Review list test", time.Date(2099, 2, 1, 0, 0, 2-i, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range ids {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1::UUID`, id)
		}
	}()

	api := &adminAPI{pool: pool}
	read := func(cursor string) struct {
		Data []struct {
			DriverID string `json:"driverId"`
		} `json:"data"`
		Meta struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"meta"`
	} {
		query := url.Values{"status": {"rejected"}, "limit": {"1"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/drivers?"+query.Encode(), nil)
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = request
		api.listDrivers(ginCtx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data []struct {
				DriverID string `json:"driverId"`
			} `json:"data"`
			Meta struct {
				NextCursor *string `json:"nextCursor"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := read("")
	if len(first.Data) != 1 || first.Data[0].DriverID != ids[0] || first.Meta.NextCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	second := read(*first.Meta.NextCursor)
	if len(second.Data) != 1 || second.Data[0].DriverID != ids[1] {
		t.Fatalf("second page=%+v", second)
	}
}
