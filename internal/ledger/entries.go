package ledger

import (
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
)

// Capture books a captured payment: the provider now holds the order total
// for us, each seller is owed their net, and the platform earns commission.
//
//	Dr psp_settlement      total
//	  Cr seller:<id>         net     (per sub-order)
//	  Cr revenue:commission  sum of commissions
func Capture(id string, o order.Order, subs []order.SubOrder) Entry {
	var commission, total money.Amount
	ps := []Posting{{}} // settlement leg filled in below
	for _, s := range subs {
		ps = append(ps, Credit(SellerPayable(s.SellerID), s.Net))
		commission += s.Commission
		total += s.Gross
	}
	ps[0] = Debit(Settlement, total)
	ps = append(ps, Credit(CommissionRevenue, commission))

	return Entry{
		ID:       id,
		Ref:      "capture:" + o.ID,
		Currency: o.Currency,
		Postings: postings(ps...),
	}
}

// Payout books a transfer to a seller's bank account.
//
//	Dr seller:<id>        amount
//	  Cr psp_settlement     amount
func Payout(id, payoutID string, seller order.SellerID, cur money.Currency, amount money.Amount) Entry {
	return Entry{
		ID:       id,
		Ref:      "payout:" + payoutID,
		Currency: cur,
		Postings: postings(Debit(SellerPayable(seller), amount), Credit(Settlement, amount)),
	}
}
