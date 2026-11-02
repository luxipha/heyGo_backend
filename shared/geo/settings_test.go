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
	base.PlaceProvider = "nominatim"
	if err := base.Validate(); err != nil {
		t.Fatalf("Nominatim search rejected: %v", err)
	}
	base.MapProvider = "nominatim"
	if base.Validate() == nil {
		t.Fatal("search provider incorrectly accepted as mobile map provider")
	}
}

func TestPlaceSwitchesAndLegacyCompatibility(t *testing.T) {
	s := Settings{PlaceProvider: "nominatim"}
	if p := s.EnabledPlaces(); !p.Nominatim || p.Google || p.Geoapify {
		t.Fatal("legacy changed")
	}
	s.PlaceProvider = "google"
	s.PlaceProviders = &PlaceProviders{Geoapify: true, Nominatim: true}
	if p := s.EnabledPlaces(); p.Google || !p.Geoapify || !p.Nominatim {
		t.Fatal("switches ignored")
	}
	s.PlaceProvider = "disabled"
	if s.PlacesEnabled() {
		t.Fatal("global disable bypassed")
	}
}
