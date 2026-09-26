package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/iam/apiv1/iampb"
	gpubsub "cloud.google.com/go/pubsub/v2"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
)

// Message is the transport-neutral message presented to domain handlers.
type Message struct {
	ID         string
	Topic      string
	Key        string
	Data       []byte
	Attributes map[string]string
}

// MessageHandler processes one Pub/Sub delivery. Returning an error causes a
// negative acknowledgement so Pub/Sub can retry and eventually dead-letter it.
type MessageHandler func(context.Context, *Message) error

// Client owns the Pub/Sub publisher and subscriber clients for one service.
type Client struct {
	client    *gpubsub.Client
	projectID string
	groupID   string

	mu         sync.Mutex
	publishers map[string]*gpubsub.Publisher

	Producer *Producer
	Consumer *Consumer
}

func NewClient(ctx context.Context, projectID, groupID string) (*Client, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("GCP_PROJECT_ID is required")
	}
	raw, err := gpubsub.NewClientWithConfig(ctx, projectID, &gpubsub.ClientConfig{EnableOpenTelemetryTracing: true})
	if err != nil {
		return nil, fmt.Errorf("create Pub/Sub client: %w", err)
	}
	c := &Client{client: raw, projectID: projectID, groupID: groupID, publishers: make(map[string]*gpubsub.Publisher)}
	c.Producer = &Producer{client: c}
	c.Consumer = &Consumer{client: c}
	return c, nil
}

func (c *Client) publisher(topic string) *gpubsub.Publisher {
	c.mu.Lock()
	defer c.mu.Unlock()
	if publisher := c.publishers[topic]; publisher != nil {
		return publisher
	}
	publisher := c.client.Publisher(topic)
	publisher.EnableMessageOrdering = true
	c.publishers[topic] = publisher
	return publisher
}

func (c *Client) Ping(ctx context.Context) error {
	topics := contracts.AllTopics()
	if len(topics) == 0 {
		return errors.New("no Pub/Sub topics configured")
	}
	permission := "pubsub.topics.publish"
	result, err := c.client.TopicAdminClient.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    fmt.Sprintf("projects/%s/topics/%s", c.projectID, topics[0]),
		Permissions: []string{permission},
	})
	if err != nil {
		return err
	}
	if len(result.Permissions) != 1 || result.Permissions[0] != permission {
		return fmt.Errorf("runtime identity lacks %s", permission)
	}
	return nil
}

func (c *Client) Close() {
	c.mu.Lock()
	for _, publisher := range c.publishers {
		publisher.Stop()
	}
	c.publishers = nil
	c.mu.Unlock()
	_ = c.client.Close()
}

// SubscriptionID is shared by the application and Terraform so every service
// consumes through its own subscription, the Pub/Sub equivalent of a Kafka
// consumer group.
func SubscriptionID(groupID, topic string) string {
	replacer := strings.NewReplacer(".", "-", "_", "-", "/", "-")
	return replacer.Replace(groupID + "-" + topic)
}

type Producer struct {
	client *Client
}

func (p *Producer) SendMessage(ctx context.Context, topic string, message *contracts.EventMessage) error {
	return p.SendMessageAndWait(ctx, topic, message, 10*time.Second)
}

func (p *Producer) SendMessageAndWait(ctx context.Context, topic string, message *contracts.EventMessage, timeout time.Duration) error {
	prepare(ctx, message)
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal event message: %w", err)
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := p.client.publisher(topic).Publish(deliveryCtx, &gpubsub.Message{
		Data:        data,
		OrderingKey: message.EntityID,
		Attributes: map[string]string{
			"event_id":       message.EventID,
			"correlation_id": message.CorrelationID,
			"event_type":     topic,
		},
	})
	if _, err := result.Get(deliveryCtx); err != nil {
		return fmt.Errorf("publish Pub/Sub message to %s: %w", topic, err)
	}
	return nil
}

func prepare(ctx context.Context, message *contracts.EventMessage) {
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

type Consumer struct {
	client *Client
}

func (c *Consumer) SubscribeAndConsume(ctx context.Context, topics []string, handler MessageHandler) error {
	if len(topics) == 0 {
		return errors.New("at least one Pub/Sub topic is required")
	}
	receiveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(topics))
	for _, topic := range topics {
		topic := topic
		go func() {
			subscriber := c.client.client.Subscriber(SubscriptionID(c.client.groupID, topic))
			subscriber.ReceiveSettings.MaxOutstandingMessages = 32
			subscriber.ReceiveSettings.MaxOutstandingBytes = 16 << 20
			errCh <- subscriber.Receive(receiveCtx, func(messageCtx context.Context, delivery *gpubsub.Message) {
				var envelope contracts.EventMessage
				if err := json.Unmarshal(delivery.Data, &envelope); err != nil {
					log.Printf("invalid Pub/Sub envelope on %s: %v", topic, err)
					delivery.Nack()
					return
				}
				if err := envelope.Validate(); err != nil {
					log.Printf("invalid Pub/Sub envelope on %s: %v", topic, err)
					delivery.Nack()
					return
				}
				if err := contracts.ValidateTopicPayload(topic, envelope.Data); err != nil {
					log.Printf("invalid Pub/Sub payload on %s: %v", topic, err)
					delivery.Nack()
					return
				}
				messageCtx = correlation.WithID(messageCtx, envelope.CorrelationID)
				if err := handler(messageCtx, &Message{ID: delivery.ID, Topic: topic, Key: envelope.EntityID, Data: delivery.Data, Attributes: delivery.Attributes}); err != nil {
					log.Printf("Pub/Sub handler failed on %s: %v", topic, err)
					delivery.Nack()
					return
				}
				delivery.Ack()
			})
		}()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}
