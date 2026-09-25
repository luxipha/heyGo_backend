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
	"github.com/google/uuid"
)

type Producer struct {
	pr *kafka.Producer
}

// NewProducer creates a confluent producer with safe defaults.
func newProducer(brokers []string) (*Producer, error) {
	cfg := &kafka.ConfigMap{
		"bootstrap.servers":  strings.Join(brokers, ","),
		"security.protocol":  "PLAINTEXT",
		"acks":               "all",
		"enable.idempotence": true,
		"batch.size":         32 * 1024, // 32KB
		"compression.type":   "zstd",
	}

	pr, err := kafka.NewProducer(cfg)
	if err != nil {
		return nil, err
	}

	go func() {
		for e := range pr.Events() {
			switch ev := e.(type) {
			case *kafka.Message:
				if ev.TopicPartition.Error != nil {
					log.Printf("Delivery failed to topic %s [%d] at offset %v: %v\n",
						*ev.TopicPartition.Topic,
						ev.TopicPartition.Partition,
						ev.TopicPartition.Offset,
						ev.TopicPartition.Error,
					)
				} else {
					log.Printf("Delivered message to topic %s [%d] at offset %v\n",
						*ev.TopicPartition.Topic,
						ev.TopicPartition.Partition,
						ev.TopicPartition.Offset,
					)
				}
			}
		}
	}()

	return &Producer{pr: pr}, nil
}

func (p *Producer) SendMessage(ctx context.Context, topic string, message *contracts.KafkaMessage) error {
	p.prepare(ctx, message)
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal the message: %w", err)
	}

	msg := &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Key:            []byte(message.EntityID),
		Value:          data,
	}

	return traces.TracedProducer(ctx, topic, msg, p.pr.Produce)
}

func (p *Producer) SendMessageAndWait(ctx context.Context, topic string, message *contracts.KafkaMessage, timeout time.Duration) error {
	p.prepare(ctx, message)
	// The producer may send a delivery report after a timeout. Never close a
	// per-message delivery channel while librdkafka still owns that send.
	deliveryChan := make(chan kafka.Event, 1)

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal the message: %w", err)
	}

	msg := &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Key:            []byte(message.EntityID),
		Value:          data,
	}

	if err := p.pr.Produce(msg, deliveryChan); err != nil {
		return fmt.Errorf("failed to produce message: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case ev := <-deliveryChan:
		m := ev.(*kafka.Message)
		if m.TopicPartition.Error != nil {
			return fmt.Errorf("delivery failed: %w", m.TopicPartition.Error)
		}
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func (p *Producer) prepare(ctx context.Context, message *contracts.KafkaMessage) {
	if message.Version == "" {
		message.Version = contracts.EventSchemaVersion
	}
	if message.EventID == "" {
		message.EventID = uuid.NewString()
	}
	if message.CorrelationID == "" {
		message.CorrelationID = correlation.FromContext(ctx)
	}
}

func (p *Producer) SendDeadLetter(ctx context.Context, sourceTopic string, message *kafka.Message, handlerErr error) error {
	topic := sourceTopic + ".dlq"
	headers := append([]kafka.Header(nil), message.Headers...)
	headers = append(headers, kafka.Header{Key: "x-source-topic", Value: []byte(sourceTopic)}, kafka.Header{Key: "x-error", Value: []byte(handlerErr.Error())})
	dlq := &kafka.Message{TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny}, Key: message.Key, Value: message.Value, Headers: headers}
	delivery := make(chan kafka.Event, 1)
	if err := p.pr.Produce(dlq, delivery); err != nil {
		return fmt.Errorf("produce dead-letter message: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case event := <-delivery:
		delivered, ok := event.(*kafka.Message)
		if !ok {
			return fmt.Errorf("unexpected dead-letter delivery event")
		}
		if delivered.TopicPartition.Error != nil {
			return fmt.Errorf("deliver dead-letter message: %w", delivered.TopicPartition.Error)
		}
		return nil
	case <-time.After(10 * time.Second):
		return context.DeadlineExceeded
	}
}

// Close flushes and shuts down the producer and listeners.
func (p *Producer) Close() {
	if p.pr != nil {
		p.pr.Flush(15 * 1000)
		p.pr.Close()
	}
}
