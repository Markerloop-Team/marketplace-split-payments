package checkout

import (
	"context"
	"errors"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/order"
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

	// RecordWebhook stores a provider event ID and reports whether it is new.
	RecordWebhook(ctx context.Context, provider, eventID string) (bool, error)

	PostEntry(ctx context.Context, e ledger.Entry) error
}
