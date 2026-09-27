package events

import "testing"

func TestPaymentSessionEventIDIsStableAndScoped(t *testing.T) {
	first := paymentSessionEventID("trip-1")
	if first != paymentSessionEventID("trip-1") {
		t.Fatal("payment session event ID changed for the same trip")
	}
	if first == paymentSessionEventID("trip-2") {
		t.Fatal("different trips received the same payment session event ID")
	}
}
