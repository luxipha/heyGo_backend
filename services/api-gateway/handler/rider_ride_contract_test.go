package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	tripproto "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRiderMutationsRequireConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		cancel     bool
		want       string
	}{{"start", `{"rideFareID":"` + uuid.NewString() + `"}`, false, "confirmation_required"}, {"cancel", `{"reason":"Plans changed"}`, true, "confirmation_required"}} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/trip/start", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = gin.Params{{Key: "tripID", Value: uuid.NewString()}}
			c.Set("authenticated-user", gatewayauth.User{ID: uuid.NewString(), Roles: []string{"rider"}})
			if tc.cancel {
				publishLifecycleCommand(c, nil, contracts.TripCmdCancel)
			} else {
				tripStartHandler(c)
			}
			if w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if riderReplayCursor("") != "evt_0" || riderReplayCursor("evt_17") != "evt_17" {
		t.Fatal("Rider replay skipped history")
	}
}
func TestRiderQuoteErrorsRemainDistinguishable(t *testing.T) {
	for reason, want := range map[string]int{"fare_invalid": 404, "fare_foreign": 403, "fare_expired": 410, "rider_restricted": 403, "debt_changed": 409, "market_unavailable": 409, "trip_service_unavailable": 503} {
		s, err := status.New(codes.FailedPrecondition, reason).WithDetails(&errdetails.ErrorInfo{Domain: "heygo.rider", Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		if !publicTripError(c, s.Err()) || w.Code != want || !strings.Contains(w.Body.String(), reason) {
			t.Fatalf("%s status=%d body=%s", reason, w.Code, w.Body.String())
		}
	}
}

func TestRiderPreviewMapsAuthoritativeGeometry(t *testing.T) {
	route := &tripproto.Route{Distance: 1000, Duration: 300, Geometry: []*tripproto.Geometry{{Coordinates: []*tripproto.Coordinate{{Latitude: 6.45, Longitude: 3.39}, {Latitude: 6.5, Longitude: 3.4}}}}}
	got, err := publicRiderRoute(route)
	if err != nil {
		t.Fatal(err)
	}
	geometry := got["geometry"].(map[string]any)
	if geometry["type"] != "LineString" || !reflect.DeepEqual(geometry["coordinates"], [][]float64{{3.39, 6.45}, {3.4, 6.5}}) || got["distance"] != float64(1000) || got["duration"] != float64(300) {
		t.Fatalf("invalid mapping: %v", got)
	}
	route.Geometry[0].Coordinates[0].Latitude = 100
	if _, err := publicRiderRoute(route); err == nil {
		t.Fatal("invalid geometry accepted")
	}
}
