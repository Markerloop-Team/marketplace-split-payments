// Package postgres implements checkout.Store and outbox.Source on PostgreSQL.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) InTx(ctx context.Context, fn func(context.Context, checkout.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(t pgx.Tx) error {
		return fn(ctx, &tx{t})
	})
}

type tx struct {
	pgx.Tx
}

func (t *tx) InsertOrder(ctx context.Context, o order.Order, subs []order.SubOrder) error {
	position := make(map[string]int, len(o.Lines))
	for i, l := range o.Lines {
		position[l.ID] = i
	}

	b := &pgx.Batch{}
	b.Queue(`INSERT INTO orders (id, customer_id, currency, discount, total, status, payment_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		o.ID, o.CustomerID, o.Currency, o.Discount, o.Total(), o.Status, nullable(o.PaymentID))
	for _, s := range subs {
		b.Queue(`INSERT INTO sub_orders (id, order_id, seller_id, rate_bps, gross, commission, net)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			s.ID, o.ID, s.SellerID, s.Rate, s.Gross, s.Commission, s.Net)
		for _, l := range s.Lines {
			b.Queue(`INSERT INTO order_lines (id, sub_order_id, position, sku, qty, unit_price, paid)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				l.ID, s.ID, position[l.ID], l.SKU, l.Qty, l.UnitPrice, l.Paid)
		}
	}
	return t.SendBatch(ctx, b).Close()
}

func (t *tx) LockOrder(ctx context.Context, id string) error {
	var one int
	err := t.QueryRow(ctx, "SELECT 1 FROM orders WHERE id = $1 FOR UPDATE", id).Scan(&one)
	return notFound(err)
}

func (t *tx) LoadOrder(ctx context.Context, id string) (order.Order, []order.SubOrder, error) {
	var (
		o         order.Order
		paymentID *string
	)
	err := t.QueryRow(ctx, `SELECT id, customer_id, currency, discount, status, payment_id
		FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.CustomerID, &o.Currency, &o.Discount, &o.Status, &paymentID)
	if err != nil {
		return order.Order{}, nil, notFound(err)
	}
	if paymentID != nil {
		o.PaymentID = *paymentID
	}

	rows, _ := t.Query(ctx, `SELECT id, seller_id, rate_bps, gross, commission, net, refunded, commission_refunded
		FROM sub_orders WHERE order_id = $1 ORDER BY id`, id)
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (order.SubOrder, error) {
		s := order.SubOrder{OrderID: id}
		err := r.Scan(&s.ID, &s.SellerID, &s.Rate, &s.Gross, &s.Commission, &s.Net, &s.Refunded, &s.CommissionRefunded)
		return s, err
	})
	if err != nil {
		return order.Order{}, nil, err
	}
	index := make(map[string]int, len(subs))
	for i, s := range subs {
		index[s.ID] = i
	}

	rows, _ = t.Query(ctx, `SELECT l.id, l.sub_order_id, s.seller_id, l.sku, l.qty, l.unit_price, l.paid, l.refunded_qty
		FROM order_lines l JOIN sub_orders s ON s.id = l.sub_order_id
		WHERE s.order_id = $1 ORDER BY l.position`, id)
	var subID string
	lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (order.Line, error) {
		var l order.Line
		err := r.Scan(&l.ID, &subID, &l.SellerID, &l.SKU, &l.Qty, &l.UnitPrice, &l.Paid, &l.RefundedQty)
		if err == nil {
			s := &subs[index[subID]]
			s.Lines = append(s.Lines, l)
		}
		return l, err
	})
	if err != nil {
		return order.Order{}, nil, err
	}
	o.Lines = lines
	return o, subs, nil
}

func (t *tx) UpdateOrderStatus(ctx context.Context, id string, s order.Status) error {
	tag, err := t.Exec(ctx, "UPDATE orders SET status = $2, updated_at = now() WHERE id = $1", id, s)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkout.ErrNotFound
	}
	return nil
}

func (t *tx) UpdateSubOrders(ctx context.Context, subs []order.SubOrder) error {
	b := &pgx.Batch{}
	for _, s := range subs {
		b.Queue("UPDATE sub_orders SET refunded = $2, commission_refunded = $3 WHERE id = $1",
			s.ID, s.Refunded, s.CommissionRefunded)
		for _, l := range s.Lines {
			b.Queue("UPDATE order_lines SET refunded_qty = $2 WHERE id = $1", l.ID, l.RefundedQty)
		}
	}
	return t.SendBatch(ctx, b).Close()
}

func (t *tx) RecordWebhook(ctx context.Context, provider, eventID string) (bool, error) {
	tag, err := t.Exec(ctx, `INSERT INTO processed_webhooks (provider, event_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, provider, eventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (t *tx) PostEntry(ctx context.Context, e ledger.Entry) error {
	b := &pgx.Batch{}
	b.Queue("INSERT INTO ledger_entries (id, ref, currency) VALUES ($1, $2, $3)", e.ID, e.Ref, e.Currency)
	for _, p := range e.Postings {
		b.Queue("INSERT INTO ledger_postings (entry_id, account, amount) VALUES ($1, $2, $3)", e.ID, p.Account, p.Amount)
	}
	return t.SendBatch(ctx, b).Close()
}

func (t *tx) Balances(ctx context.Context, cur money.Currency) (ledger.Balances, error) {
	rows, _ := t.Query(ctx, `SELECT p.account, sum(p.amount)::bigint
		FROM ledger_postings p JOIN ledger_entries e ON e.id = p.entry_id
		WHERE e.currency = $1 GROUP BY p.account`, cur)
	b := ledger.Balances{}
	var (
		acc ledger.Account
		amt money.Amount
	)
	_, err := pgx.ForEachRow(rows, []any{&acc, &amt}, func() error {
		b[acc] = amt
		return nil
	})
	return b, err
}

func (t *tx) LockPayouts(ctx context.Context, cur money.Currency) error {
	_, err := t.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('payouts:' || $1::text))", cur)
	return err
}

func (t *tx) InsertPayout(ctx context.Context, p payout.Payout) error {
	_, err := t.Exec(ctx, `INSERT INTO payouts (id, seller_id, currency, amount, status)
		VALUES ($1, $2, $3, $4, $5)`, p.ID, p.SellerID, p.Currency, p.Amount, p.Status)
	return err
}

type refundPart struct {
	SubOrderID string             `json:"sub_order_id"`
	SellerID   order.SellerID     `json:"seller_id"`
	Lines      []order.ReturnLine `json:"lines"`
	Amount     money.Amount       `json:"amount"`
	Commission money.Amount       `json:"commission"`
	Net        money.Amount       `json:"net"`
}

func (t *tx) FindRefund(ctx context.Context, orderID, key string) (order.Refund, bool, error) {
	r := order.Refund{OrderID: orderID, IdempotencyKey: key}
	var raw []byte
	err := t.QueryRow(ctx, `SELECT id, amount, parts FROM refunds
		WHERE order_id = $1 AND idempotency_key = $2`, orderID, key).Scan(&r.ID, &r.Amount, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Refund{}, false, nil
	}
	if err != nil {
		return order.Refund{}, false, err
	}
	var parts []refundPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return order.Refund{}, false, fmt.Errorf("decode refund %s: %w", r.ID, err)
	}
	for _, p := range parts {
		r.Parts = append(r.Parts, order.RefundPart(p))
	}
	return r, true, nil
}

func (t *tx) InsertRefund(ctx context.Context, r order.Refund) error {
	parts := make([]refundPart, len(r.Parts))
	for i, p := range r.Parts {
		parts[i] = refundPart(p)
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO refunds (id, order_id, idempotency_key, amount, parts)
		VALUES ($1, $2, $3, $4, $5)`, r.ID, r.OrderID, r.IdempotencyKey, r.Amount, raw)
	return err
}

func (t *tx) Enqueue(ctx context.Context, msgs ...outbox.Message) error {
	b := &pgx.Batch{}
	for _, m := range msgs {
		b.Queue("INSERT INTO outbox (topic, key, payload) VALUES ($1, $2, $3)", m.Topic, m.Key, m.Payload)
	}
	return t.SendBatch(ctx, b).Close()
}

// Claim implements outbox.Source. Rows stay locked while fn publishes them,
// and SKIP LOCKED lets several relays share the table without blocking.
func (s *Store) Claim(ctx context.Context, limit int, fn func([]outbox.Message) error) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(t pgx.Tx) error {
		rows, _ := t.Query(ctx, `SELECT id, topic, key, payload, created_at FROM outbox
			WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (outbox.Message, error) {
			var m outbox.Message
			err := r.Scan(&m.ID, &m.Topic, &m.Key, &m.Payload, &m.CreatedAt)
			return m, err
		})
		if err != nil || len(msgs) == 0 {
			return err
		}
		if err := fn(msgs); err != nil {
			return err
		}
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		if _, err := t.Exec(ctx, "UPDATE outbox SET published_at = now() WHERE id = ANY($1)", ids); err != nil {
			return err
		}
		n = len(msgs)
		return nil
	})
	return n, err
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return checkout.ErrNotFound
	}
	return err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
