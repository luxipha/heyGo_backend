package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
)

// Consumer wraps a Kafka consumer.
type Consumer struct {
	cr         *kafka.Consumer // Kafka consumer instance
	producer   *Producer
	maxRetries int
}

// NewConsumer creates a confluent consumer with safe defaults.
func newConsumer(brokers []string, groupID string, producer *Producer) (*Consumer, error) {
	cfg := &kafka.ConfigMap{
		"bootstrap.servers":        strings.Join(brokers, ","),
		"group.id":                 groupID,
		"auto.offset.reset":        "earliest",
		"enable.auto.commit":       false,
		"session.timeout.ms":       6000,
		"allow.auto.create.topics": false,
	}

	cr, err := kafka.NewConsumer(cfg)
	if err != nil {
		return nil, err
	}

	return &Consumer{cr: cr, producer: producer, maxRetries: 3}, nil
}

// MessageHandler defines the function signature for processing Kafka messages.
type MessageHandler func(context.Context, *kafka.Message) error

// subscribeAndConsume subscribes to the given topic and processes messages using the provided handler.
func (c *Consumer) SubscribeAndConsume(ctx context.Context, topics []string, handler MessageHandler) error {
	if err := c.cr.SubscribeTopics(topics, nil); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			e := c.cr.Poll(100)
			if e == nil {
				continue
			}
			switch ev := e.(type) {
			case *kafka.Message:
				var envelope contracts.KafkaMessage
				if err := json.Unmarshal(ev.Value, &envelope); err != nil || envelope.Validate() != nil {
					if err == nil {
						err = envelope.Validate()
					}
					if dlqErr := c.producer.SendDeadLetter(ctx, *ev.TopicPartition.Topic, ev, err); dlqErr != nil {
						return fmt.Errorf("publish invalid message to DLQ: %w", dlqErr)
					}
					if _, err := c.cr.CommitMessage(ev); err != nil {
						return err
					}
					continue
				}
				if err := contracts.ValidateTopicPayload(*ev.TopicPartition.Topic, envelope.Data); err != nil {
					if dlqErr := c.producer.SendDeadLetter(correlation.WithID(ctx, envelope.CorrelationID), *ev.TopicPartition.Topic, ev, err); dlqErr != nil {
						return fmt.Errorf("publish invalid payload to DLQ: %w", dlqErr)
					}
					if _, err := c.cr.CommitMessage(ev); err != nil {
						return err
					}
					continue
				}
				var handlerErr error
				for attempt := 0; attempt <= c.maxRetries; attempt++ {
					handlerErr = traces.TracedConsumer(ev, func(traceCtx context.Context, msg *kafka.Message) error {
						return handler(correlation.WithID(traceCtx, envelope.CorrelationID), msg)
					})
					if handlerErr == nil {
						break
					}
					if attempt < c.maxRetries {
						time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
					}
				}
				if handlerErr != nil {
					log.Printf("Message handler exhausted retries: %v", handlerErr)
					if err := c.producer.SendDeadLetter(correlation.WithID(ctx, envelope.CorrelationID), *ev.TopicPartition.Topic, ev, handlerErr); err != nil {
						return err
					}
				}
				if ev.Headers != nil {
					log.Printf("Headers: %v\n", ev.Headers)
				}
				if _, err := c.cr.CommitMessage(ev); err != nil {
					log.Printf("Failed to commit message: %v", err)
				}
			case kafka.Error:
				log.Printf("Kafka error: %v, code: %v\n", ev, ev.Code())
			default:
				log.Printf("Ignored event: %v\n", ev)
			}
		}
	}
}

// Close shuts down the consumer.
func (c *Consumer) Close() {
	if c.cr != nil {
		c.cr.Close()
	}
}
