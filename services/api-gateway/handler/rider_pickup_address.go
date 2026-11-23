package handler

import (
	"errors"
	"strings"
	"unicode"

	"github.com/luxipha/heyGo_backend/services/api-gateway/types"
)

func devicePickupMetadata(p types.PreviewTripRequest) (map[string]any, error) {
	name, address := strings.TrimSpace(p.PickupName), strings.TrimSpace(p.PickupAddress)
	for _, value := range []string{name, address} {
		if len([]rune(value)) > 500 || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, errors.New("Pickup address allows at most 500 characters without control characters")
		}
	}
	if name == "" {
		name = address
	}
	if name == "" {
		name = "Current location"
	}
	// Display-only device geocoding never overrides coordinates or resolves a
	// client-supplied place ID. Provider places are resolved separately as before.
	return map[string]any{"provider": "device", "name": name, "address": address, "location": p.Pickup}, nil
}
