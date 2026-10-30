package checkout

import (
	"context"
	"errors"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

var ErrNotFound = errors.New("not found")

// Store runs fn in a single database transaction. Everything a use case
// writes — order state, ledger entries, events — commits or rolls back
// together.
type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

type Tx interface {
	InsertOrder(ctx context.Context, o order.Order, subs []order.SubOrder) error
	// LockOrder serialises writers on one order until the transaction ends.
	LockOrder(ctx context.Context, id string) error
	LoadOrder(ctx context.Context, id string) (order.Order, []order.SubOrder, error)
	UpdateOrderStatus(ctx context.Context, id string, s order.Status) error
	// UpdateSubOrders persists refund counters on sub-orders and lines.
	UpdateSubOrders(ctx context.Context, subs []order.SubOrder) error

	FindRefund(ctx context.Context, orderID, idempotencyKey string) (order.Refund, bool, error)
	InsertRefund(ctx context.Context, r order.Refund) error

	// RecordWebhook stores a provider event ID and reports whether it is new.
	RecordWebhook(ctx context.Context, provider, eventID string) (bool, error)

	PostEntry(ctx context.Context, e ledger.Entry) error
	Balances(ctx context.Context, cur money.Currency) (ledger.Balances, error)

	// LockPayouts serialises payout runs for a currency.
	LockPayouts(ctx context.Context, cur money.Currency) error
	InsertPayout(ctx context.Context, p payout.Payout) error

	Enqueue(ctx context.Context, msgs ...outbox.Message) error
}
