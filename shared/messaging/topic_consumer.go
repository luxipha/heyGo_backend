package messaging

import (
	"context"
	"encoding/json"
	"log"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
)

type TopicConsumer struct {
	bus     *pubsub.Client
	connMgr *ConnectionManager
	topics  []string
	events  *EventStore
}

func NewTopicConsumer(bus *pubsub.Client, connMgr *ConnectionManager, topics []string, events ...*EventStore) *TopicConsumer {
	var store *EventStore
	if len(events) > 0 {
		store = events[0]
	}
	return &TopicConsumer{
		bus:     bus,
		connMgr: connMgr,
		topics:  topics,
		events:  store,
	}
}

func (tc *TopicConsumer) Consume(ctx context.Context) error {
	return tc.bus.Consumer.SubscribeAndConsume(ctx, tc.topics,
		func(ctx context.Context, msg *pubsub.Message) error {
			var eventMsg contracts.EventMessage
			if err := json.Unmarshal(msg.Data, &eventMsg); err != nil {
				log.Printf("Failed to unmarshal message: %v", err)
				return err
			}

			entityID := eventMsg.EntityID

			var payload any
			if eventMsg.Data != nil {
				if err := json.Unmarshal(eventMsg.Data, &payload); err != nil {
					log.Printf("Failed to unmarshal payload: %v", err)
					return err
				}
			}

			clientMsg := contracts.WSMessage{
				Type: msg.Topic,
				Data: payload,
			}
			if tc.events != nil {
				sourceID := eventMsg.EventID
				if sourceID == "" {
					sourceID = "pubsub:" + msg.ID
				}
				stored, err := tc.events.Append(ctx, entityID, sourceID, clientMsg.Type, eventMsg.Data)
				if err != nil {
					return err
				}
				clientMsg = stored
			}

			if tc.events != nil {
				return nil // Each gateway delivers from the shared event log in cursor order.
			}
			return tc.connMgr.SendMessage(entityID, clientMsg)
		},
	)
}
