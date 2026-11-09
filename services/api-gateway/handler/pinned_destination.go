package handler

import (
	"errors"
	"fmt"
	"github.com/luxipha/heyGo_backend/services/api-gateway/types"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
)

// The coordinate identity is internal metadata, never a provider place ID.
// Routing and service-map checks still use the exact user-confirmed point.
func pinnedPreviewDestination(p types.PreviewTripRequest) (*agent.Place, error) {
	if p.SchemaVersion != 1 || p.DestinationSource != "pin" || p.DestinationPlaceID != "" || !validCoordinate(p.Destination.Latitude, p.Destination.Longitude) {
		return nil, errors.New("Confirm valid destination coordinates and omit destinationPlaceId for a pin")
	}
	return &agent.Place{ID: fmt.Sprintf("pin:%.7f,%.7f", p.Destination.Latitude, p.Destination.Longitude), Name: "Pinned destination", Provider: "pin", Location: agent.Coordinate{Latitude: p.Destination.Latitude, Longitude: p.Destination.Longitude}}, nil
}
