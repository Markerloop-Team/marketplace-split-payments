// Package payout decides what each seller is paid from their ledger balance.
package payout

import (
	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
)

type Status string

// StatusRequested means the transfer instruction has been handed to the bank.
const StatusRequested Status = "requested"

type Payout struct {
	ID       string
	SellerID order.SellerID
	Currency money.Currency
	Amount   money.Amount
	Status   Status
}

type Policy struct {
	// Minimum keeps tiny transfers from being eaten by bank fees; smaller
	// balances roll over to the next run.
	Minimum money.Amount
}

// Plan pays every seller whose payable balance reaches the minimum. Sellers
// with a negative balance (refunds after a payout) are skipped; the debt is
// netted against their future sales.
func Plan(b ledger.Balances, p Policy) []ledger.SellerBalance {
	var out []ledger.SellerBalance
	for _, sb := range b.Payables() {
		if sb.Amount >= p.Minimum && sb.Amount > 0 {
			out = append(out, sb)
		}
	}
	return out
}
