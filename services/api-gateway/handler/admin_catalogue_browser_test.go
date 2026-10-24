package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCatalogueBrowserServer is an opt-in local-only authenticated QA harness.
func TestCatalogueBrowserServer(t *testing.T) {
	if os.Getenv("CATALOGUE_BROWSER_QA") != "1" {
		t.Skip("local browser harness disabled")
	}
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "55439" || !strings.HasPrefix(u.Path, "/heygo_catalogue_browser_") {
		t.Fatal("isolated local database required")
	}
	pool, err := shareddb.NewPostgresPool(context.Background(), shareddb.Config{URL: u.String(), MaxConns: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	creds := []map[string]string{}
	for _, label := range []string{"creator", "approver"} {
		id, password := uuid.NewString(), uuid.NewString()
		username := "catalogue-" + label + "-" + id
		hash, _ := adminauth.HashPassword(password)
		if _, err = pool.Exec(context.Background(), `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,$3,'super_admin')`, id, username, hash); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(context.Background(), `INSERT INTO admin_user_roles(admin_id,role) VALUES($1::UUID,'super_admin') ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		creds = append(creds, map[string]string{"id": id, "username": username, "password": password})
	}
	driver := uuid.NewString()
	if _, err = pool.Exec(context.Background(), `INSERT INTO users(id,roles) VALUES($1::UUID,ARRAY['driver'])`, driver); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driver); err != nil {
		t.Fatal(err)
	}
	market := "CATALOGUE_QA"
	if _, err = pool.Exec(context.Background(), `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,'market',$2,$2,1,ST_GeomFromText('MULTIPOLYGON(((100 20,101 20,101 21,100 21,100 20)))',4326),'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, uuid.NewString(), market, creds[0]["id"], creds[1]["id"]); err != nil {
		t.Fatal(err)
	}
	driverToken := uuid.NewString() + uuid.NewString()
	if _, err = pool.Exec(context.Background(), `INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1::UUID,$2::UUID,$3,$4,NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours')`, uuid.NewString(), driver, gatewayauth.TokenHash(driverToken), gatewayauth.TokenHash(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"accounts": creds, "driverId": driver, "marketCode": market, "driverToken": driverToken})
	if err = os.WriteFile("/private/tmp/catalogue-browser-session.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_COOKIE_SECURE", "false")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "http://localhost:4002")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Headers", "Content-Type,X-CSRF-Token")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})
	store := newCatalogueQAStore()
	router.Any("/_qa/objects/:ticket", store.serve)
	registerAdminRoutes(router, pool, store)
	authMiddleware := gatewayauth.NewSessionMiddleware(pool, gatewayauth.NewPostgresUserStore(pool))
	authenticated := router.Group("", authMiddleware.Authenticate)
	registerDriverRoutes(authenticated, pool, store)
	server := &http.Server{Addr: "127.0.0.1:18081", Handler: router}
	go func() { _ = server.ListenAndServe() }()
	t.Log("Local authenticated catalogue browser server ready on 18081")
	<-time.After(20 * time.Minute)
	_ = server.Close()
}
