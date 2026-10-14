package service

import (
	"testing"

	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
)

func TestCalculateFareKoboUsesMetersAndSeconds(t *testing.T) {
	rate := repo.PackageRate{BaseKobo: 100, PerKmKobo: 200, PerMinuteKobo: 60}
	got, valid := calculateFareKobo(1200, 300, rate)
	if !valid || got != 640 {
		t.Fatalf("fare=%v valid=%v, want 640 kobo", got, valid)
	}
	for _, input := range []struct{ distance, duration float64 }{{-1, 60}, {1, -1}, {1e9, 60}} {
		if _, valid := calculateFareKobo(input.distance, input.duration, rate); valid {
			t.Fatalf("accepted invalid route %+v", input)
		}
	}
}
