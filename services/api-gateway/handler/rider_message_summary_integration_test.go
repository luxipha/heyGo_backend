package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRiderMessageSummaryOwnershipCountsAndPagination(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	owner, driver, other, oldDriver := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, driver, other, oldDriver} {
		exec(`INSERT INTO users(id,roles) VALUES($1::uuid,ARRAY['rider','driver'])`, id)
	}
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status,available) VALUES($1::uuid,'Emeka Okafor','TEST','sedan','available',true)`, driver)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	g := router.Group("/rider", func(c *gin.Context) {
		role := "rider"
		if c.GetHeader("X-Owner") == driver {
			role = "driver"
		}
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Owner"), Roles: []string{role}})
	}, gatewayauth.RequireRole("rider"))
	registerRiderNotificationRoutes(g, &riderAccountAPI{pool: pool})
	call := func(who, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Owner", who)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	summary := func(who, path string) (riderMessagesSummary, *string) {
		t.Helper()
		w := call(who, "GET", path)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Data riderMessagesSummary `json:"data"`
			Meta struct {
				Next *string `json:"nextCursor"`
			} `json:"meta"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out.Data, out.Meta.Next
	}
	var newest string
	for i := 0; i < 35; i++ {
		if e := pool.QueryRow(ctx, `INSERT INTO rider_notifications(rider_id,type,title,message,data,source_event_id) VALUES($1::uuid,'promotions',$2,'offer','{}',gen_random_uuid()::text) RETURNING id::text`, owner, fmt.Sprintf("Offer %d", i)).Scan(&newest); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO rider_notifications(rider_id,type,title,message,data,read_at,source_event_id) VALUES($1::uuid,'feedback','Tell us','Survey','{}',NOW(),gen_random_uuid()::text)`, owner)
	exec(`INSERT INTO rider_notifications(rider_id,type,title,message,data,source_event_id) VALUES($1::uuid,'ride_updates','Driver arrived','Hidden channel','{}',gen_random_uuid()::text),($2::uuid,'promotions','Foreign secret','Hidden','{}',gen_random_uuid()::text)`, owner, other)
	makeTrip := func(who, status string, days int, hasMessage bool) string {
		t.Helper()
		id, fare := uuid.NewString(), uuid.NewString()
		exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::uuid,$2::uuid,'sedan',10000,'{}')`, fare, who)
		exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,updated_at,completed_at,cancelled_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,NOW(),CASE WHEN $5='completed' THEN NOW()-make_interval(days=>$6) END,CASE WHEN $5='cancelled' THEN NOW()-make_interval(days=>$6) END)`, id, who, fare, driver, status, days)
		if hasMessage {
			exec(`INSERT INTO trip_chat_messages(id,trip_id,sender_id,recipient_id,client_message_id,body) VALUES(gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,gen_random_uuid(),'Hello')`, id, driver, who)
		}
		return id
	}
	active := makeTrip(owner, "started", 0, false)
	for i := 0; i < 60; i++ {
		exec(`INSERT INTO trip_chat_messages(id,trip_id,sender_id,recipient_id,client_message_id,body,created_at) VALUES(gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,gen_random_uuid(),'Incoming',NOW()-INTERVAL '1 minute')`, active, driver, owner)
	}
	exec(`INSERT INTO trip_chat_messages(id,trip_id,sender_id,recipient_id,client_message_id,body) VALUES(gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,gen_random_uuid(),'Latest outgoing')`, active, owner, driver)
	exec(`INSERT INTO trip_chat_messages(id,trip_id,sender_id,recipient_id,client_message_id,body) VALUES(gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,gen_random_uuid(),'OLD DRIVER SECRET')`, active, oldDriver, owner)
	for i := 0; i < 21; i++ {
		makeTrip(owner, "completed", 1, true)
	}
	makeTrip(owner, "cancelled", 40, true)
	makeTrip(owner, "completed", 40, true)
	makeTrip(owner, "cancelled", 2, false)
	foreign := makeTrip(other, "started", 0, true)
	out, next := summary(owner, "/rider/messages/summary")
	if len(out.Channels) != 3 || len(out.PastTrips) != 20 || next == nil || out.CurrentTrip == nil || out.CurrentTrip.TripID != active || out.CurrentTrip.UnreadCount != 60 || *out.CurrentTrip.LastMessage != "Latest outgoing" || out.TotalUnread != 116 {
		t.Fatalf("summary %+v next %v", out, next)
	}
	for _, c := range out.Channels {
		if c.Type == "promotions" && (c.UnreadCount != 35 || c.Latest.ID != newest) {
			t.Fatal(c)
		}
	}
	if !strings.Contains(call(owner, "GET", "/rider/messages/summary").Header().Get("Cache-Control"), "no-store") {
		t.Fatal("cacheable")
	}
	seen := map[string]bool{}
	for _, c := range out.PastTrips {
		seen[c.TripID] = true
	}
	page, end := summary(owner, "/rider/messages/summary?cursor="+*next)
	if len(page.PastTrips) != 1 || end != nil || seen[page.PastTrips[0].TripID] {
		t.Fatal("pagination", page, end)
	}
	otherOut, _ := summary(other, "/rider/messages/summary")
	if otherOut.CurrentTrip == nil || otherOut.CurrentTrip.TripID != foreign || len(otherOut.PastTrips) != 0 {
		t.Fatal("owner isolation", otherOut)
	}
	if call(driver, "GET", "/rider/messages/summary").Code != 403 || call(owner, "GET", "/rider/messages/summary?cursor=bad").Code != 400 {
		t.Fatal("role/cursor")
	}
	w := call(owner, "GET", "/rider/notifications?type=feedback")
	if w.Code != 200 || strings.Contains(w.Body.String(), "Offer") || !strings.Contains(w.Body.String(), "Tell us") {
		t.Fatal("filtered history", w.Body.String())
	}
	if call(owner, "GET", "/rider/notifications?type=not_a_channel").Code != 400 {
		t.Fatal("invalid channel")
	}
	if call(other, "PATCH", "/rider/notifications/"+newest).Code != 404 {
		t.Fatal("foreign read")
	}
	if call(owner, "PATCH", "/rider/notifications/"+newest).Code != 200 {
		t.Fatal("read failed")
	}
	out, _ = summary(owner, "/rider/messages/summary")
	if out.TotalUnread != 115 {
		t.Fatal("read did not refresh exact total", out.TotalUnread)
	}
	exec(`UPDATE trip_chat_messages SET read_at=NOW() WHERE trip_id=$1::uuid AND sender_id=$2::uuid AND recipient_id=$3::uuid`, active, driver, owner)
	out, _ = summary(owner, "/rider/messages/summary")
	if out.CurrentTrip.UnreadCount != 0 || out.TotalUnread != 55 {
		t.Fatal("trip read counts", out)
	}
}
