package contracts

import (
	"encoding/json"
	"fmt"
)

// ValidateTopicPayload enforces the required top-level fields for each version-1
// event payload. Consumers use it before dispatch so malformed messages go to
// the topic's dead-letter queue instead of reaching business logic.
func ValidateTopicPayload(topic string, data []byte) error {
	var payload map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &payload) != nil || payload == nil {
		return fmt.Errorf("%s payload must be a JSON object", topic)
	}
	require := func(fields ...string) error {
		for _, field := range fields {
			value, ok := payload[field]
			if !ok || len(value) == 0 || string(value) == "null" || string(value) == `""` {
				return fmt.Errorf("%s payload requires %s", topic, field)
			}
		}
		return nil
	}

	switch topic {
	case TripEventCreated, TripEventArrived, TripEventStarted, TripEventCompleted, TripEventCancelled,
		TripEventNoDriversFound, TripEventDriverNotInterested:
		return require("trip")
	case TripEventSettlementUpdated:
		return require("tripId", "status", "expectedAmountKobo")
	case TripEventNoShowClaimUpdated:
		return require("tripId", "claimId", "status", "feeKobo")
	case DriverCmdTripRequest:
		return require("trip", "offer")
	case TripEventExpired, TripEventReassigned:
		return require("tripId", "status")
	case DriverEventCommandAcknowledged:
		return require("tripId", "command", "status")
	case TripEventDriverAssigned:
		return require("id", "riderID", "driver")
	case DriverCmdTripAccept, DriverCmdTripDecline:
		return require("tripID", "driver")
	case DriverCmdLocation, DriverEventLocationUpdated:
		return require("location")
	case DriverCmdRegister:
		return require("id")
	case PaymentCmdCreateSession:
		return require("tripID", "riderID", "driverID", "amount", "currency")
	case PaymentEventSessionCreated:
		return require("tripID", "sessionID", "paymentReference", "checkoutURL", "amount", "currency")
	case PaymentEventSuccess, PaymentEventFailed, PaymentEventCancelled:
		return require("eventID", "tripID", "paymentReference", "status")
	case TripCmdArrive, TripCmdStart, TripCmdComplete:
		return require("tripID", "actorID")
	case TripCmdCancel:
		return require("tripID", "actorID", "reason")
	case TripCmdRate:
		return require("tripID", "actorID", "rating")
	default:
		return fmt.Errorf("unsupported topic %q", topic)
	}
}
