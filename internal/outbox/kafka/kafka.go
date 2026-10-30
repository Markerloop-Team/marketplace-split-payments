// Package kafka publishes outbox messages to Kafka (or Redpanda).
package kafka

import (
	"context"
	"fmt"
	"strconv"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/markerloop/marketplace-split-payments/internal/outbox"
)

// HeaderMessageID carries the outbox row ID; consumers use it to drop
// redeliveries.
const HeaderMessageID = "message-id"

type Publisher struct {
	client *kgo.Client
}

// New connects an idempotent producer that waits for all in-sync replicas.
func New(brokers []string) (*Publisher, error) {
	c, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	return &Publisher{client: c}, nil
}

func (p *Publisher) Publish(ctx context.Context, msgs []outbox.Message) error {
	recs := make([]*kgo.Record, len(msgs))
	for i, m := range msgs {
		recs[i] = &kgo.Record{
			Topic:     m.Topic,
			Key:       []byte(m.Key),
			Value:     m.Payload,
			Timestamp: m.CreatedAt,
			Headers: []kgo.RecordHeader{
				{Key: HeaderMessageID, Value: []byte(strconv.FormatInt(m.ID, 10))},
			},
		}
	}
	return p.client.ProduceSync(ctx, recs...).FirstErr()
}

func (p *Publisher) Close() { p.client.Close() }
