// Package outbox implements the transactional outbox: events are written to a
// table in the same transaction as the state change that caused them, and a
// relay publishes them afterwards. Delivery is at least once; consumers
// deduplicate by message ID.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Message struct {
	ID        int64
	Topic     string
	Key       string
	Payload   []byte
	CreatedAt time.Time
}

// New encodes v as the JSON payload. Key decides partitioning, so all events
// for one order (or one seller) stay ordered.
func New(topic, key string, v any) (Message, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Message{}, fmt.Errorf("outbox: encode %s: %w", topic, err)
	}
	return Message{Topic: topic, Key: key, Payload: b}, nil
}

type Publisher interface {
	Publish(ctx context.Context, msgs []Message) error
}

// Source hands out unpublished messages.
type Source interface {
	// Claim locks up to limit pending messages, calls fn with them and marks
	// them published only if fn returns nil.
	Claim(ctx context.Context, limit int, fn func([]Message) error) (int, error)
}

type Relay struct {
	src      Source
	pub      Publisher
	batch    int
	interval time.Duration
	log      *slog.Logger
}

func NewRelay(src Source, pub Publisher, log *slog.Logger) *Relay {
	return &Relay{src: src, pub: pub, batch: 100, interval: 500 * time.Millisecond, log: log}
}

// Drain publishes until nothing is pending.
func (r *Relay) Drain(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := r.src.Claim(ctx, r.batch, func(msgs []Message) error {
			return r.pub.Publish(ctx, msgs)
		})
		total += n
		if err != nil || n < r.batch {
			return total, err
		}
	}
}

// Run drains on every tick until ctx is cancelled. Publish failures are
// logged and retried on the next tick; nothing is lost because unpublished
// rows stay in the table.
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		if _, err := r.Drain(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("outbox relay", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// MemoryPublisher collects messages; set Err to simulate a broker outage.
type MemoryPublisher struct {
	mu   sync.Mutex
	msgs []Message
	Err  error
}

func (p *MemoryPublisher) Publish(_ context.Context, msgs []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Err != nil {
		return p.Err
	}
	p.msgs = append(p.msgs, msgs...)
	return nil
}

func (p *MemoryPublisher) Messages() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Message(nil), p.msgs...)
}

// LogPublisher writes messages to a logger; handy when no broker is configured.
type LogPublisher struct{ Log *slog.Logger }

func (p LogPublisher) Publish(_ context.Context, msgs []Message) error {
	for _, m := range msgs {
		p.Log.Info("event", "id", m.ID, "topic", m.Topic, "key", m.Key, "payload", string(m.Payload))
	}
	return nil
}
