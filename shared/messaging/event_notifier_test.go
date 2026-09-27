package messaging

import (
	"testing"
	"time"
)

func TestEventNotifierFansOutAndUnsubscribes(t *testing.T) {
	notifier := &EventNotifier{subscribers: make(map[string]map[chan struct{}]struct{})}
	first, unsubscribeFirst := notifier.Subscribe("driver-1")
	second, unsubscribeSecond := notifier.Subscribe("driver-1")
	defer unsubscribeSecond()

	notifier.notify("driver-1")
	for i, wake := range []<-chan struct{}{first, second} {
		select {
		case <-wake:
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d was not notified", i)
		}
	}

	unsubscribeFirst()
	notifier.notify("driver-1")
	select {
	case <-first:
		t.Fatal("unsubscribed listener was notified")
	default:
	}
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("remaining subscriber was not notified")
	}
}
