package types

import (
	"github.com/luxipha/heyGo_backend/shared/geo"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestTrafficMetadataSurvivesTripProtocol(t *testing.T) {
	response := OSRMApiResponse{Provider: "google", Routes: []geo.Route{{Distance: 1400, Duration: 420, StaticDuration: 300, TrafficAware: true, Geometry: geo.Geometry{Coordinates: [][]float64{{3.39, 6.45}, {3.4, 6.5}}}}}}
	data, err := proto.Marshal(response.ToProto())
	if err != nil {
		t.Fatal(err)
	}
	var route pb.Route
	if err = proto.Unmarshal(data, &route); err != nil {
		t.Fatal(err)
	}
	if route.Provider != "google" || !route.TrafficAware || route.StaticDuration != 300 || route.Duration != 420 || route.Geometry[0].Coordinates[0].Latitude != 6.45 {
		t.Fatalf("lost route metadata: %v", &route)
	}
}
