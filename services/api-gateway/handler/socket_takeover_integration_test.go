package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	"github.com/cprakhar/uber-clone/shared/contracts"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func TestDriverSocketTakeoverAcrossGatewayReplicasReplaysOnce(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	t.Setenv("LOG_ROTATE", "false")
	if _, err := logs.Init("api-gateway-socket-takeover-test"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 6})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	driverID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, driverID, "socket-test-"+driverID, "socket-test-"+driverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, driverID); err != nil {
		t.Fatal(err)
	}
	users := contractTestUsers{id: driverID}
	makeReplica := func() (*httptest.Server, *messaging.ConnectionManager) {
		manager := messaging.NewConnectionManager("https://app.heygo.test")
		router := gin.New()
		router.Use(gatewayauth.NewMiddleware(contractTestVerifier{}, users).Authenticate)
		router.GET("/ws/drivers", func(c *gin.Context) { DriversWSHandler(c, nil, manager, pool) })
		return httptest.NewServer(router), manager
	}
	replicaA, _ := makeReplica()
	replicaB, _ := makeReplica()
	t.Cleanup(replicaA.Close)
	t.Cleanup(replicaB.Close)
	dial := func(server *httptest.Server, after string) *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/drivers"
		if after != "" {
			url += "?afterEventId=" + after
		}
		header := http.Header{"Origin": []string{"https://app.heygo.test"}, "Authorization": []string{"Bearer test"}}
		conn, response, err := websocket.DefaultDialer.Dial(url, header)
		if err != nil {
			if response != nil {
				t.Fatalf("dial gateway replica: %v (HTTP %s)", err, response.Status)
			}
			t.Fatalf("dial gateway replica: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	readEvent := func(conn *websocket.Conn, timeout time.Duration) contracts.WSMessage {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		var event contracts.WSMessage
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("read socket event: %v", err)
		}
		return event
	}

	clientA := dial(replicaA, "evt_0")
	registrationA := readEvent(clientA, 3*time.Second)
	if registrationA.Type != contracts.DriverCmdRegister {
		t.Fatalf("first replica registration event type=%q", registrationA.Type)
	}

	clientB := dial(replicaB, "evt_0")
	registrationB := readEvent(clientB, 3*time.Second)
	if registrationB.ID != registrationA.ID || registrationB.Type != registrationA.Type {
		t.Fatalf("takeover replay=%#v, first registration=%#v", registrationB, registrationA)
	}
	ownRegistrationB := readEvent(clientB, 3*time.Second)
	if ownRegistrationB.Type != contracts.DriverCmdRegister || ownRegistrationB.ID == registrationB.ID {
		t.Fatalf("second replica's live registration=%#v", ownRegistrationB)
	}

	_ = clientA.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := clientA.ReadMessage(); err == nil {
		t.Fatal("replaced socket remained open after its next ownership poll")
	}

	store := messaging.NewEventStore(pool)
	if _, err := store.Append(ctx, driverID, uuid.NewString(), "test.event", []byte(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	live := readEvent(clientB, 3*time.Second)
	if live.Type != "test.event" {
		t.Fatalf("live event after takeover=%#v", live)
	}

	clientReplay := dial(replicaA, ownRegistrationB.ID)
	replayed := readEvent(clientReplay, 3*time.Second)
	if replayed.ID != live.ID || replayed.Type != live.Type {
		t.Fatalf("replay after takeover=%#v, want %#v", replayed, live)
	}
	if next := readEvent(clientReplay, 3*time.Second); next.ID == replayed.ID {
		t.Fatal("replayed event was delivered more than once")
	}
}
