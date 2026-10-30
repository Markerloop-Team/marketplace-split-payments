package checkout

import (
	"context"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

const TopicPayoutRequested = "payouts.requested"

type PayoutEvent struct {
	PayoutID string         `json:"payout_id"`
	SellerID order.SellerID `json:"seller_id"`
	Amount   money.Amount   `json:"amount"`
	Currency money.Currency `json:"currency"`
}

// RunPayouts settles seller balances in one currency. Concurrent runs are
// serialised so a balance is never paid twice; the bank transfer itself is
// done by the consumer of payouts.requested.
func (s *Service) RunPayouts(ctx context.Context, cur money.Currency, policy payout.Policy) ([]payout.Payout, error) {
	var out []payout.Payout
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		out = nil
		if err := tx.LockPayouts(ctx, cur); err != nil {
			return err
		}
		balances, err := tx.Balances(ctx, cur)
		if err != nil {
			return err
		}
		for _, sb := range payout.Plan(balances, policy) {
			p := payout.Payout{
				ID:       "po_" + s.newID(),
				SellerID: sb.SellerID,
				Currency: cur,
				Amount:   sb.Amount,
				Status:   payout.StatusRequested,
			}
			if err := tx.InsertPayout(ctx, p); err != nil {
				return err
			}
			if err := s.postEntry(ctx, tx, ledger.Payout(s.newID(), p.ID, p.SellerID, cur, p.Amount)); err != nil {
				return err
			}
			msg, err := outbox.New(TopicPayoutRequested, string(p.SellerID), PayoutEvent{
				PayoutID: p.ID, SellerID: p.SellerID, Amount: p.Amount, Currency: cur,
			})
			if err != nil {
				return err
			}
			if err := tx.Enqueue(ctx, msg); err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
