package traces

import (
	"context"
	"encoding/json"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// kafkaHeadersCarrier implements the TextMapCarrier interface for Kafka headers
type kafkaHeadersCarrier []ckafka.Header

func (c *kafkaHeadersCarrier) Get(key string) string {
	for _, header := range *c {
		if string(header.Key) == key {
			return string(header.Value)
		}
	}
	return ""
}

func (c *kafkaHeadersCarrier) Set(key string, value string) {
	// Remove existing header with the same key
	for i, header := range *c {
		if string(header.Key) == key {
			(*c)[i].Value = []byte(value)
			return
		}
	}
	// Add new header if not found
	*c = append(*c, ckafka.Header{Key: key, Value: []byte(value)})
}

func (c *kafkaHeadersCarrier) Keys() []string {
	keys := make([]string, 0, len(*c))
	for _, header := range *c {
		keys = append(keys, string(header.Key))
	}
	return keys
}

// TracedProducer wraps the Kafka produce function with tracing
func TracedProducer(ctx context.Context, topic string, msg *ckafka.Message, produce func(*ckafka.Message, chan ckafka.Event) error) error {
	tracer := otel.GetTracerProvider().Tracer("apache-kafka")

	ctx, span := tracer.Start(ctx, "apache-kafka.produce",
		trace.WithAttributes(
			attribute.String("messaging.system", "apache-kafka"),
			attribute.String("messaging.destination", topic),
			attribute.String("messaging.operation", "publish"),
		),
	)
	defer span.End()

	// Try to extract and add message details to span
	var msgData contracts.KafkaMessage
	if err := json.Unmarshal(msg.Value, &msgData); err == nil {
		if msgData.EntityID != "" {
			span.SetAttributes(attribute.String("messaging.kafka.message.entity_id", msgData.EntityID))
		}
	}

	// Inject trace context into message headers
	if msg.Headers == nil {
		msg.Headers = []ckafka.Header{}
	}
	carrier := kafkaHeadersCarrier(msg.Headers)
	otel.GetTextMapPropagator().Inject(ctx, &carrier)
	msg.Headers = []ckafka.Header(carrier)

	if err := produce(msg, nil); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	return nil
}

// TracedConsumer wraps the Kafka message handler with tracing
func TracedConsumer(msg *ckafka.Message, handler func(context.Context, *ckafka.Message) error) error {
	// Extract trace context from message headers
	var carrier kafkaHeadersCarrier
	if msg.Headers != nil {
		carrier = kafkaHeadersCarrier(msg.Headers)
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), &carrier)

	tracer := otel.GetTracerProvider().Tracer("apache-kafka")

	topic := ""
	if msg.TopicPartition.Topic != nil {
		topic = *msg.TopicPartition.Topic
	}

	ctx, span := tracer.Start(ctx, "apache-kafka.consume",
		trace.WithAttributes(
			attribute.String("messaging.system", "apache-kafka"),
			attribute.String("messaging.destination", topic),
			attribute.String("messaging.operation", "receive"),
			attribute.Int("messaging.kafka.partition", int(msg.TopicPartition.Partition)),
			attribute.Int64("messaging.kafka.offset", int64(msg.TopicPartition.Offset)),
		),
	)
	defer span.End()

	// Try to extract and add message details to span
	var msgData contracts.KafkaMessage
	if err := json.Unmarshal(msg.Value, &msgData); err == nil {
		if msgData.EntityID != "" {
			span.SetAttributes(attribute.String("messaging.kafka.message.entity_id", msgData.EntityID))
		}
	}

	if err := handler(ctx, msg); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	return nil
}

// Legacy function for backward compatibility - will use TracedProducer internally
func TracedSendMessage(ctx context.Context, topic string, msg *ckafka.Message, produceFunc func(*ckafka.Message, chan ckafka.Event) error) error {
	return TracedProducer(ctx, topic, msg, produceFunc)
}
