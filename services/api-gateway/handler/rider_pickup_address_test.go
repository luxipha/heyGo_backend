package handler

import (
	"strings"
	"testing"

	"github.com/luxipha/heyGo_backend/services/api-gateway/types"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
)

func TestDevicePickupAddressPreservesCoordinateAuthority(t *testing.T) {
	point := sharedtypes.Coordinate{Latitude: 6.578, Longitude: 3.359}
	meta, err := devicePickupMetadata(types.PreviewTripRequest{Pickup: point, PickupName: " 15 Isaac John St ", PickupAddress: "15 Isaac John St, Ikeja, Lagos"})
	if err != nil || meta["name"] != "15 Isaac John St" || meta["address"] != "15 Isaac John St, Ikeja, Lagos" || meta["location"] != point || meta["provider"] != "device" {
		t.Fatalf("resolved address lost or coordinates changed: %v %v", meta, err)
	}
	for _, value := range []string{strings.Repeat("x", 501), "bad\naddress", "bad\x00address"} {
		if _, err := devicePickupMetadata(types.PreviewTripRequest{PickupAddress: value}); err == nil {
			t.Fatal("invalid pickup label accepted")
		}
	}
}
