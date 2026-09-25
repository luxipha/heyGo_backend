package messaging

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cprakhar/uber-clone/shared/contracts"
)

type InboxNotification struct {
	Type    string
	Title   string
	Message string
	Data    json.RawMessage
}

func notificationForDriverEvent(kind string, raw json.RawMessage) *InboxNotification {
	var data map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&data)
	str := func(key string) string { value, _ := data[key].(string); return strings.TrimSpace(value) }
	amount := func(key string) string {
		value, ok := data[key]
		if !ok {
			return ""
		}
		if number, ok := value.(json.Number); ok {
			kobo, err := strconv.ParseInt(number.String(), 10, 64)
			if err == nil {
				naira, remainder := kobo/100, kobo%100
				if remainder < 0 {
					remainder = -remainder
				}
				if kobo < 0 && naira == 0 {
					return fmt.Sprintf("-0.%02d", remainder)
				}
				return fmt.Sprintf("%d.%02d", naira, remainder)
			}
		}
		return fmt.Sprint(value)
	}
	makeMessage := func(typ, title, body string) *InboxNotification {
		return &InboxNotification{Type: typ, Title: title, Message: body, Data: raw}
	}
	switch kind {
	case "driver.document.updated":
		label, status := humanizeNotificationValue(str("type")), humanizeNotificationValue(str("status"))
		if label == "" {
			label = "Document"
		}
		if status == "" {
			status = "updated"
		}
		body := label + " status: " + status + "."
		if reason := str("reason"); reason != "" {
			body += " " + reason
		}
		return makeMessage("verification", "Document status updated", body)
	case "driver.inspection.updated":
		status := humanizeNotificationValue(str("status"))
		if status == "" {
			status = "updated"
		}
		body := "Vehicle inspection status: " + status + "."
		if reason := str("reason"); reason != "" {
			body += " " + reason
		}
		return makeMessage("inspection", "Inspection status updated", body)
	case "driver.onboarding.updated":
		status := humanizeNotificationValue(str("adminStatus"))
		if status == "" {
			status = humanizeNotificationValue(str("status"))
		}
		if status == "" {
			status = "updated"
		}
		body := "Your driver approval status is " + status + "."
		if reason := str("reason"); reason != "" {
			body += " " + reason
		}
		return makeMessage("verification", "Driver status updated", body)
	case contracts.DriverCmdTripRequest:
		return makeMessage("trip", "New ride request", "A rider has requested a trip. Open HeyGo to review the offer.")
	case contracts.TripEventDriverAssigned:
		return makeMessage("trip", "Trip assigned", "You have been assigned a trip.")
	case contracts.TripEventExpired:
		return makeMessage("trip", "Trip offer expired", "The trip offer is no longer available.")
	case contracts.TripEventReassigned:
		return makeMessage("trip", "Trip reassigned", "The trip offer is no longer assigned to you.")
	case contracts.TripEventCompleted:
		return makeMessage("trip", "Trip completed", "Your trip has been marked complete.")
	case contracts.TripEventCancelled:
		return makeMessage("trip", "Trip cancelled", "Your trip was cancelled.")
	case contracts.TripEventSettlementUpdated:
		status := humanizeNotificationValue(str("status"))
		if status == "" {
			status = "updated"
		}
		body := "Trip payment status: " + status + "."
		if value := amount("expectedAmountKobo"); value != "" {
			body = "Trip payment status: " + status + " (₦" + value + ")."
		}
		return makeMessage("trip", "Trip payment updated", body)
	case contracts.TripEventNoShowClaimUpdated:
		status := humanizeNotificationValue(str("status"))
		if status == "" {
			status = "updated"
		}
		body := "Your no-show claim status: " + status + "."
		if value := amount("feeKobo"); value != "" {
			body += " Fee: ₦" + value + "."
		}
		return makeMessage("trip", "No-show claim updated", body)
	case "driver.operating_balance.updated":
		updateKind := humanizeNotificationValue(str("kind"))
		if updateKind == "" {
			updateKind = "balance"
		}
		body := "Your Operating Balance has been updated."
		if value := amount("deltaKobo"); value != "" {
			body = "Operating Balance " + strings.ToLower(updateKind) + ": ₦" + value + "."
		}
		return makeMessage("balance", "Operating Balance updated", body)
	default:
		return nil
	}
}

func humanizeNotificationValue(value string) string {
	if value == "" {
		return ""
	}
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(value))
	for i, word := range words {
		if word != "" {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
