package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
)

const TopicRefundCreated = "refunds.created"

var ErrNotRefundable = errors.New("order is not refundable")

type RefundInput struct {
	OrderID string
	// IdempotencyKey is chosen by the caller; retrying with the same key
	// returns the original refund instead of refunding twice.
	IdempotencyKey string
	Lines          []order.ReturnLine
}

type RefundEvent struct {
	RefundID string         `json:"refund_id"`
	OrderID  string         `json:"order_id"`
	Amount   money.Amount   `json:"amount"`
	Currency money.Currency `json:"currency"`
	Parts    []RefundPart   `json:"parts"`
}

type RefundPart struct {
	SubOrderID string         `json:"sub_order_id"`
	SellerID   order.SellerID `json:"seller_id"`
	Amount     money.Amount   `json:"amount"`
	Commission money.Amount   `json:"commission"`
	Net        money.Amount   `json:"net"`
}

// Refund returns some items of a paid order to the customer.
//
// The provider is called while the order row is locked and before commit,
// with an idempotency key derived from the caller's key. If the commit fails
// after the provider accepted the refund, the caller retries with the same
// key, the provider treats it as a duplicate, and we book it exactly once.
func (s *Service) Refund(ctx context.Context, in RefundInput) (order.Refund, error) {
	if in.IdempotencyKey == "" {
		return order.Refund{}, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}

	var r order.Refund
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.LockOrder(ctx, in.OrderID); err != nil {
			return err
		}
		existing, found, err := tx.FindRefund(ctx, in.OrderID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			r = existing
			return nil
		}

		o, subs, err := tx.LoadOrder(ctx, in.OrderID)
		if err != nil {
			return err
		}
		if o.Status != order.StatusPaid && o.Status != order.StatusPartiallyRefunded {
			return fmt.Errorf("%w: status %s", ErrNotRefundable, o.Status)
		}

		parts, err := order.QuoteRefund(subs, in.Lines)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		r = order.Refund{ID: "rf_" + s.newID(), OrderID: o.ID, IdempotencyKey: in.IdempotencyKey, Parts: parts}
		for _, p := range parts {
			r.Amount += p.Amount
		}

		err = s.provider.Refund(ctx, payment.RefundRequest{
			PaymentID:      o.PaymentID,
			Amount:         r.Amount,
			IdempotencyKey: o.ID + "/" + in.IdempotencyKey,
		})
		if err != nil {
			return fmt.Errorf("provider refund: %w", err)
		}

		subs = order.ApplyRefund(subs, parts)
		status := order.StatusPartiallyRefunded
		if order.FullyRefunded(subs) {
			status = order.StatusRefunded
		}
		if err := tx.UpdateSubOrders(ctx, subs); err != nil {
			return err
		}
		if err := tx.UpdateOrderStatus(ctx, o.ID, status); err != nil {
			return err
		}
		if err := tx.InsertRefund(ctx, r); err != nil {
			return err
		}
		if err := s.postEntry(ctx, tx, ledger.Refund(s.newID(), r, o.Currency)); err != nil {
			return err
		}
		msg, err := outbox.New(TopicRefundCreated, o.ID, refundEvent(r, o.Currency))
		if err != nil {
			return err
		}
		return tx.Enqueue(ctx, msg)
	})
	return r, err
}

func refundEvent(r order.Refund, cur money.Currency) RefundEvent {
	ev := RefundEvent{RefundID: r.ID, OrderID: r.OrderID, Amount: r.Amount, Currency: cur}
	for _, p := range r.Parts {
		ev.Parts = append(ev.Parts, RefundPart{
			SubOrderID: p.SubOrderID,
			SellerID:   p.SellerID,
			Amount:     p.Amount,
			Commission: p.Commission,
			Net:        p.Net,
		})
	}
	return ev
}
