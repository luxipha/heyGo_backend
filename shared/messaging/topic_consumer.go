package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
)

type TopicConsumer struct {
	kf      *kafka.KafkaClient
	connMgr *ConnectionManager
	topics  []string
	events  *EventStore
}

func NewTopicConsumer(kf *kafka.KafkaClient, connMgr *ConnectionManager, topics []string, events ...*EventStore) *TopicConsumer {
	var store *EventStore
	if len(events) > 0 {
		store = events[0]
	}
	return &TopicConsumer{
		kf:      kf,
		connMgr: connMgr,
		topics:  topics,
		events:  store,
	}
}

func (tc *TopicConsumer) Consume(ctx context.Context) error {
	return tc.kf.Consumer.SubscribeAndConsume(ctx, tc.topics,
		func(ctx context.Context, msg *ckafka.Message) error {
			var kfMsg contracts.KafkaMessage
			if err := json.Unmarshal(msg.Value, &kfMsg); err != nil {
				log.Printf("Failed to unmarshal message: %v", err)
				return err
			}

			entityID := kfMsg.EntityID

			var payload any
			if kfMsg.Data != nil {
				if err := json.Unmarshal(kfMsg.Data, &payload); err != nil {
					log.Printf("Failed to unmarshal payload: %v", err)
					return err
				}
			}

			clientMsg := contracts.WSMessage{
				Type: *msg.TopicPartition.Topic,
				Data: payload,
			}
			if tc.events != nil {
				sourceID := kfMsg.EventID
				if sourceID == "" {
					sourceID = fmt.Sprintf("legacy:%s:%d:%d", clientMsg.Type, msg.TopicPartition.Partition, msg.TopicPartition.Offset)
				}
				stored, err := tc.events.Append(ctx, entityID, sourceID, clientMsg.Type, kfMsg.Data)
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
