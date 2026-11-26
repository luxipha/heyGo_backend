package handler

import "testing"

func TestDriverTripPlaceFromMetadata(t *testing.T) {
	stored := []byte(`{"id":"ChIJ","name":"Obafemi Awolowo Wy","address":"Obafemi Awolowo Wy, Alausa, Lagos","location":{"latitude":6.61,"longitude":3.35},"provider":"google"}`)
	place := driverTripPlaceFromMetadata(stored)
	if place == nil || place.Name != "Obafemi Awolowo Wy" || place.Address != "Obafemi Awolowo Wy, Alausa, Lagos" {
		t.Fatalf("unexpected place: %+v", place)
	}
	// Device pickups have a name but no address.
	device := driverTripPlaceFromMetadata([]byte(`{"name":"Current location","address":"","provider":"device"}`))
	if device == nil || device.Name != "Current location" || device.Address != "" {
		t.Fatalf("unexpected device place: %+v", device)
	}
	for name, raw := range map[string][]byte{"nil": nil, "empty": {}, "null": []byte("null"), "no text": []byte(`{"provider":"device"}`), "not json": []byte("x")} {
		if got := driverTripPlaceFromMetadata(raw); got != nil {
			t.Errorf("%s: expected nil, got %+v", name, got)
		}
	}
}

// The wire format must never expose coordinates or provider ids.
func TestDriverTripPlaceDoesNotExposeCoordinates(t *testing.T) {
	place := driverTripPlaceFromMetadata([]byte(`{"name":"A","address":"B","location":{"latitude":1,"longitude":2},"id":"secret"}`))
	if place == nil {
		t.Fatal("expected a place")
	}
	if *place != (driverTripPlace{Name: "A", Address: "B"}) {
		t.Fatalf("unexpected fields: %+v", place)
	}
}
