// Package memory is an in-process Store for tests and local experiments.
// Transactions are serialised and copy-on-write, so a failed callback leaves
// no trace — the same contract the Postgres store gives.
package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
)

type Store struct {
	mu sync.Mutex
	st state
}

type orderRow struct {
	order order.Order
	subs  []order.SubOrder
}

type state struct {
	orders   map[string]orderRow
	webhooks map[string]bool
	entries  []ledger.Entry
	refs     map[string]bool
	outbox   []pending
	nextID   int64
}

type pending struct {
	msg       outbox.Message
	published bool
}

func New() *Store {
	return &Store{st: state{
		orders:   map[string]orderRow{},
		webhooks: map[string]bool{},
		refs:     map[string]bool{},
	}}
}

func (s *Store) InTx(ctx context.Context, fn func(context.Context, checkout.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := &tx{st: s.st.clone()}
	if err := fn(ctx, t); err != nil {
		return err
	}
	s.st = t.st
	return nil
}

// Entries returns every posted ledger entry.
func (s *Store) Entries() []ledger.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.st.entries)
}

// Balances sums the book for one currency.
func (s *Store) Balances(cur money.Currency) ledger.Balances {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.balances(cur)
}

func (st state) balances(cur money.Currency) ledger.Balances {
	b := ledger.Balances{}
	for _, e := range st.entries {
		if e.Currency == cur {
			b.Apply(e)
		}
	}
	return b
}

func (st state) clone() state {
	c := state{
		orders:   make(map[string]orderRow, len(st.orders)),
		webhooks: cloneSet(st.webhooks),
		entries:  slices.Clone(st.entries),
		refs:     cloneSet(st.refs),
		outbox:   slices.Clone(st.outbox),
		nextID:   st.nextID,
	}
	for k, v := range st.orders {
		c.orders[k] = cloneRow(v)
	}
	return c
}

func cloneSet(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func cloneRow(r orderRow) orderRow {
	r.order.Lines = slices.Clone(r.order.Lines)
	r.subs = slices.Clone(r.subs)
	for i := range r.subs {
		r.subs[i].Lines = slices.Clone(r.subs[i].Lines)
	}
	return r
}

type tx struct {
	st state
}

func (t *tx) InsertOrder(_ context.Context, o order.Order, subs []order.SubOrder) error {
	if _, ok := t.st.orders[o.ID]; ok {
		return fmt.Errorf("memory: order %s exists", o.ID)
	}
	t.st.orders[o.ID] = cloneRow(orderRow{order: o, subs: subs})
	return nil
}

func (t *tx) LockOrder(_ context.Context, id string) error {
	if _, ok := t.st.orders[id]; !ok {
		return checkout.ErrNotFound
	}
	return nil
}

func (t *tx) LoadOrder(_ context.Context, id string) (order.Order, []order.SubOrder, error) {
	r, ok := t.st.orders[id]
	if !ok {
		return order.Order{}, nil, checkout.ErrNotFound
	}
	r = cloneRow(r)
	// Sub-orders own the line state; the order keeps cart order.
	byID := map[string]order.Line{}
	for _, s := range r.subs {
		for _, l := range s.Lines {
			byID[l.ID] = l
		}
	}
	for i, l := range r.order.Lines {
		r.order.Lines[i] = byID[l.ID]
	}
	return r.order, r.subs, nil
}

func (t *tx) UpdateOrderStatus(_ context.Context, id string, s order.Status) error {
	r, ok := t.st.orders[id]
	if !ok {
		return checkout.ErrNotFound
	}
	r.order.Status = s
	t.st.orders[id] = r
	return nil
}

func (t *tx) RecordWebhook(_ context.Context, provider, eventID string) (bool, error) {
	key := provider + "/" + eventID
	if t.st.webhooks[key] {
		return false, nil
	}
	t.st.webhooks[key] = true
	return true, nil
}

func (t *tx) PostEntry(_ context.Context, e ledger.Entry) error {
	if t.st.refs[e.Ref] {
		return fmt.Errorf("memory: ledger ref %s already posted", e.Ref)
	}
	t.st.refs[e.Ref] = true
	e.Postings = slices.Clone(e.Postings)
	t.st.entries = append(t.st.entries, e)
	return nil
}

func (t *tx) Enqueue(_ context.Context, msgs ...outbox.Message) error {
	for _, m := range msgs {
		t.st.nextID++
		m.ID = t.st.nextID
		m.CreatedAt = time.Now()
		t.st.outbox = append(t.st.outbox, pending{msg: m})
	}
	return nil
}

// Claim implements outbox.Source.
func (s *Store) Claim(_ context.Context, limit int, fn func([]outbox.Message) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var idx []int
	var batch []outbox.Message
	for i, p := range s.st.outbox {
		if len(batch) == limit {
			break
		}
		if !p.published {
			idx = append(idx, i)
			batch = append(batch, p.msg)
		}
	}
	if len(batch) == 0 {
		return 0, nil
	}
	if err := fn(batch); err != nil {
		return 0, err
	}
	for _, i := range idx {
		s.st.outbox[i].published = true
	}
	return len(batch), nil
}
