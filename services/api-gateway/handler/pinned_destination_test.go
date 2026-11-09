package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/services/api-gateway/types"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPinnedDestinationUsesExactCoordinates(t *testing.T) {
	p := types.PreviewTripRequest{SchemaVersion: 1, DestinationSource: "pin", Pickup: sharedtypes.Coordinate{Latitude: 6.44, Longitude: 3.42}, Destination: sharedtypes.Coordinate{Latitude: 6.43123456789, Longitude: 3.45123456789}}
	place, e := pinnedPreviewDestination(p)
	if e != nil || place.Name != "Pinned destination" || place.Provider != "pin" || place.Location.Latitude != p.Destination.Latitude || place.Location.Longitude != p.Destination.Longitude {
		t.Fatalf("%+v %v", place, e)
	}
	proto := p.ToProto("rider")
	if proto.Destination.Latitude != place.Location.Latitude || proto.Destination.Longitude != place.Location.Longitude {
		t.Fatal("routing coordinates changed")
	}
	raw, _ := json.Marshal(place)
	if !strings.Contains(string(raw), `"provider":"pin"`) {
		t.Fatal(string(raw))
	}
	p.DestinationPlaceID = "provider-result"
	if _, e = pinnedPreviewDestination(p); e == nil {
		t.Fatal("ambiguous identity accepted")
	}
	p.DestinationPlaceID = ""
	p.Destination.Latitude = math.NaN()
	if _, e = pinnedPreviewDestination(p); e == nil {
		t.Fatal("invalid point accepted")
	}
	p.Destination.Latitude = 91
	if _, e = pinnedPreviewDestination(p); e == nil {
		t.Fatal("out of range accepted")
	}
}

func TestPreviewDestinationValidationBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct{ source, id, point, want string }{
		{"", "", `{"latitude":6.43,"longitude":3.45}`, "destination_unconfirmed"},
		{"other", "", `{"latitude":6.43,"longitude":3.45}`, "invalid_destination_source"},
		{"pin", "place", `{"latitude":6.43,"longitude":3.45}`, "invalid_pinned_destination"},
		{"pin", "", `{"latitude":91,"longitude":3.45}`, "invalid_route"},
		{"pin", "", `{"latitude":6.44,"longitude":3.42}`, "invalid_route"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"schemaVersion":1,"pickup":{"latitude":6.44,"longitude":3.42},"destination":` + tt.point + `,"destinationSource":"` + tt.source + `","destinationPlaceId":"` + tt.id + `"}`
		c.Request = httptest.NewRequest("POST", "/trip/preview", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		previewTripHandler(c, nil)
		if w.Code != 400 || !strings.Contains(w.Body.String(), tt.want) {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}
