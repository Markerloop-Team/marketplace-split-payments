package checkout

import (
	"context"
	"fmt"
	"net/http"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
)

// HandleWebhook applies a provider event exactly once.
//
// Providers deliver at least once and retry on any non-2xx, sometimes
// concurrently. The event ID is recorded in the same transaction as its
// effects, so a redelivery either finds the ID and does nothing, or waits on
// the first delivery's row lock and then finds it.
func (s *Service) HandleWebhook(ctx context.Context, h http.Header, body []byte) error {
	ev, err := s.provider.ParseWebhook(h, body)
	if err != nil {
		return err
	}

	return s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		fresh, err := tx.RecordWebhook(ctx, s.provider.Name(), ev.ID)
		if err != nil || !fresh {
			return err
		}
		if err := tx.LockOrder(ctx, ev.OrderID); err != nil {
			return err
		}
		o, subs, err := tx.LoadOrder(ctx, ev.OrderID)
		if err != nil {
			return err
		}
		if o.PaymentID != ev.PaymentID {
			return fmt.Errorf("%w: event %s is for payment %s", ErrPaymentMismatch, ev.ID, ev.PaymentID)
		}
		// A capture after a decline (or vice versa) is a provider-side
		// anomaly that needs a human; we never flip a settled order.
		if o.Status != order.StatusPendingPayment {
			return nil
		}

		switch ev.Type {
		case payment.EventCaptured:
			if ev.Amount != o.Total() || ev.Currency != o.Currency {
				return fmt.Errorf("%w: captured %d %s, order total %d %s",
					ErrPaymentMismatch, ev.Amount, ev.Currency, o.Total(), o.Currency)
			}
			if err := tx.UpdateOrderStatus(ctx, o.ID, order.StatusPaid); err != nil {
				return err
			}
			if err := s.postEntry(ctx, tx, ledger.Capture(s.newID(), o, subs)); err != nil {
				return err
			}
			paid, err := orderEvent(TopicOrderPaid, o)
			if err != nil {
				return err
			}
			perSeller, err := subOrderEvents(o, subs)
			if err != nil {
				return err
			}
			return tx.Enqueue(ctx, append([]outbox.Message{paid}, perSeller...)...)
		case payment.EventFailed:
			if err := tx.UpdateOrderStatus(ctx, o.ID, order.StatusPaymentFailed); err != nil {
				return err
			}
			failed, err := orderEvent(TopicOrderPaymentFailed, o)
			if err != nil {
				return err
			}
			return tx.Enqueue(ctx, failed)
		default:
			return nil
		}
	})
}
