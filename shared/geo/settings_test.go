package geo

import (
	"testing"
)

func TestAdminEndpointValidation(t *testing.T) {
	base := Settings{RouteProvider: "osrm", PlaceProvider: "google", MapProvider: "google", NavigationProvider: "google", OSRMEndpoint: "https://routing.example.com/route/v1/driving"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://metadata.google.internal/route/v1/driving", "https://127.0.0.1/route/v1/driving", "https://[::1]/route/v1/driving", "https://localhost/route/v1/driving", "https://user:secret@routing.example.com/route/v1/driving", "https://routing.example.com/route/v1/driving?key=secret", "https://routing.example.com:8080/route/v1/driving", "http://router.project-osrm.org/route/v1/driving"} {
		s := base
		s.OSRMEndpoint = endpoint
		if s.Validate() == nil {
			t.Errorf("unsafe endpoint accepted %s", endpoint)
		}
	}
	base.OSRMEndpoint = "http://router.project-osrm.org/route/v1/driving"
	base.AllowDemo = true
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.PlaceProvider = "amap"
	if base.Validate() == nil {
		t.Fatal("unimplemented adapter accepted")
	}
}
