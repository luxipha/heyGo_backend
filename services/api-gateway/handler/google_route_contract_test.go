package handler

import (
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"testing"
)

func TestPublicRiderRouteTrafficContract(t *testing.T) {
	r := &pb.Route{Distance: 1400, Duration: 420, StaticDuration: 300, TrafficAware: true, Provider: "google", Geometry: []*pb.Geometry{{Coordinates: []*pb.Coordinate{{Latitude: 6.45, Longitude: 3.39}, {Latitude: 6.5, Longitude: 3.4}}}}}
	result, err := publicRiderRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	if result["trafficAware"] != true || result["staticDuration"] != float64(300) || result["duration"] != float64(420) || result["provider"] != "google" {
		t.Fatalf("lost traffic fields: %v", result)
	}
	r.TrafficAware = false
	r.StaticDuration = 0
	r.Provider = ""
	if _, err = publicRiderRoute(r); err != nil {
		t.Fatal("legacy route rejected", err)
	}
}
