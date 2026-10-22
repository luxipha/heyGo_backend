package agent

import (
	"fmt"
	"os"
	"strings"
)

// PlaceService is the stable search/resolve boundary; alternative adapters remain disabled.
type PlaceService = Provider

func ConfiguredPlaces() (PlaceService, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("GEO_PLACE_PROVIDER")))
	if provider == "" {
		provider = "google"
	}
	if provider == "disabled" {
		return nil, nil
	}
	if provider != "google" {
		return nil, fmt.Errorf("unsupported places provider")
	}
	key := strings.TrimSpace(os.Getenv("RIDE_AGENT_PLACES_KEY"))
	if key == "" {
		return nil, fmt.Errorf("Google Places key is required")
	}
	return GooglePlaces{Key: key}, nil
}
