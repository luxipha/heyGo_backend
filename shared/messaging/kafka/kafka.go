package kafka

import (
	"context"
	"fmt"
	"strings"
	"time"

	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/cprakhar/uber-clone/shared/contracts"
)

// KafkaClient is a wrapper around Kafka producer and consumer.
type KafkaClient struct {
	Producer *Producer // Kafka producer
	Consumer *Consumer // Kafka consumer
}

// NewKafkaClient creates a new KafkaClient with the given brokers and group ID.
func NewKafkaClient(brokers []string, groupID string) (*KafkaClient, error) {
	p, err := newProducer(brokers)
	if err != nil {
		return nil, err
	}

	c, err := newConsumer(brokers, groupID, p)
	if err != nil {
		p.Close()
		return nil, err
	}

	return &KafkaClient{
		Producer: p,
		Consumer: c,
	}, nil
}

func (kc *KafkaClient) EnsureTopics(ctx context.Context, brokers []string, topics []string, partitions, replication int) error {
	admin, err := ckafka.NewAdminClient(&ckafka.ConfigMap{"bootstrap.servers": strings.Join(brokers, ",")})
	if err != nil {
		return fmt.Errorf("create Kafka admin client: %w", err)
	}
	defer admin.Close()
	specs := make([]ckafka.TopicSpecification, 0, len(topics)*2)
	for _, topic := range topics {
		specs = append(specs, ckafka.TopicSpecification{Topic: topic, NumPartitions: partitions, ReplicationFactor: replication}, ckafka.TopicSpecification{Topic: topic + ".dlq", NumPartitions: partitions, ReplicationFactor: replication})
	}
	provisionCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	results, err := admin.CreateTopics(provisionCtx, specs)
	if err != nil {
		return fmt.Errorf("provision Kafka topics: %w", err)
	}
	for _, result := range results {
		if result.Error.Code() != ckafka.ErrNoError && result.Error.Code() != ckafka.ErrTopicAlreadyExists {
			return fmt.Errorf("provision Kafka topic %s: %s", result.Topic, result.Error.String())
		}
	}
	return nil
}

func DefaultTopics() []string { return contracts.AllTopics() }

func (kc *KafkaClient) Ping(ctx context.Context) error {
	if kc == nil || kc.Producer == nil || kc.Producer.pr == nil {
		return fmt.Errorf("Kafka producer is unavailable")
	}
	done := make(chan error, 1)
	go func() { _, err := kc.Producer.pr.GetMetadata(nil, false, 3000); done <- err }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (kc *KafkaClient) Close() {
	if kc.Producer != nil {
		kc.Producer.Close()
	}
	if kc.Consumer != nil {
		kc.Consumer.Close()
	}
}
