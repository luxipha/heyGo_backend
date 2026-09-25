package contracts

import "testing"

func TestKafkaMessageSchemaValidation(t *testing.T) {
	valid := KafkaMessage{Version: EventSchemaVersion, EventID: "event-1", EntityID: "entity-1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for name, message := range map[string]KafkaMessage{"version": {EventID: "event", EntityID: "entity"}, "eventID": {Version: EventSchemaVersion, EntityID: "entity"}, "entityID": {Version: EventSchemaVersion, EventID: "event"}} {
		t.Run(name, func(t *testing.T) {
			if err := message.Validate(); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}
