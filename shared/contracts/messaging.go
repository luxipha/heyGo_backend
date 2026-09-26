package contracts

import "fmt"

// EventMessage is the versioned transport-neutral envelope used for commands
// and events.
type EventMessage struct {
	Version       string `json:"version"`
	EventID       string `json:"eventID"`
	CorrelationID string `json:"correlationID,omitempty"`
	EntityID      string `json:"entityID"`
	Data          []byte `json:"data"`
}

const EventSchemaVersion = "1"

func (m EventMessage) Validate() error {
	if m.Version != EventSchemaVersion {
		return fmt.Errorf("unsupported event schema version %q", m.Version)
	}
	if m.EventID == "" {
		return fmt.Errorf("eventID is required")
	}
	if m.EntityID == "" {
		return fmt.Errorf("entityID is required")
	}
	return nil
}

func AllTopics() []string {
	return []string{
		TripEventCreated, TripEventDriverAssigned, TripEventArrived, TripEventStarted, TripEventCompleted, TripEventCancelled, TripEventSettlementUpdated, TripEventNoShowClaimUpdated, TripEventNoDriversFound, TripEventDriverNotInterested, TripEventExpired, TripEventReassigned,
		DriverCmdTripRequest, DriverCmdTripAccept, DriverCmdTripDecline, DriverCmdLocation, DriverCmdRegister, DriverEventLocationUpdated,
		DriverEventCommandAcknowledged,
		PaymentEventSessionCreated, PaymentEventSuccess, PaymentEventFailed, PaymentEventCancelled, PaymentCmdCreateSession,
		TripCmdArrive, TripCmdStart, TripCmdComplete, TripCmdCancel, TripCmdRate,
	}
}

// Event and Command Types
const (
	// Trip events (trip.event.*)
	TripEventCreated               = "trip.event.created"
	TripEventDriverAssigned        = "trip.event.driver_assigned"
	TripEventStarted               = "trip.event.started"
	TripEventArrived               = "trip.event.arrived"
	TripEventSettlementUpdated     = "trip.event.settlement_updated"
	TripEventNoShowClaimUpdated    = "trip.event.no_show_claim_updated"
	TripEventNoDriversFound        = "trip.event.no_drivers_found"
	TripEventDriverNotInterested   = "trip.event.driver_not_interested"
	TripEventCompleted             = "trip.event.completed"
	TripEventCancelled             = "trip.event.cancelled"
	TripEventExpired               = "trip.event.expired"
	TripEventReassigned            = "trip.event.reassigned"
	DriverEventLocationUpdated     = "driver.event.location_updated"
	DriverEventCommandAcknowledged = "driver.event.command_acknowledged"

	TripCmdArrive   = "trip.cmd.arrive"
	TripCmdStart    = "trip.cmd.start"
	TripCmdComplete = "trip.cmd.complete"
	TripCmdCancel   = "trip.cmd.cancel"
	TripCmdRate     = "trip.cmd.rate"

	// Driver commands (driver.cmd.*)
	DriverCmdTripRequest = "driver.cmd.trip_request"
	DriverCmdTripAccept  = "driver.cmd.trip_accept"
	DriverCmdTripDecline = "driver.cmd.trip_decline"
	DriverCmdLocation    = "driver.cmd.location"
	DriverCmdRegister    = "driver.cmd.register"

	// Payment events (payment.event.*)
	PaymentEventSessionCreated = "payment.event.session_created"
	PaymentEventSuccess        = "payment.event.success"
	PaymentEventFailed         = "payment.event.failed"
	PaymentEventCancelled      = "payment.event.cancelled"

	// Payment commands (payment.cmd.*)
	PaymentCmdCreateSession = "payment.cmd.create_session"
)
