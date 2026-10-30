package order

import (
	"fmt"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

// SubOrder is the part of an order fulfilled and paid out to a single seller.
type SubOrder struct {
	ID       string
	OrderID  string
	SellerID SellerID
	Lines    []Line
	Rate     money.BPS
	// Gross is what the customer paid for this seller's lines.
	Gross money.Amount
	// Commission is the platform's cut, charged on Gross and rounded half up
	// once per sub-order, not per line.
	Commission money.Amount
	// Net is what the platform owes the seller: Gross - Commission.
	Net money.Amount

	// Refunded and CommissionRefunded are running totals of returns.
	Refunded           money.Amount
	CommissionRefunded money.Amount
}

// Split allocates the order discount across lines in proportion to their
// subtotals and groups lines into one sub-order per seller, in the order the
// sellers first appear in the cart. Each sub-order is charged its seller's
// commission rate.
//
// The discount is allocated per line rather than per seller so that a later
// partial return knows exactly what each unit cost the customer.
func Split(o Order, rates Rates) ([]SubOrder, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}

	weights := make([]int64, len(o.Lines))
	for i, l := range o.Lines {
		weights[i] = int64(l.Subtotal())
	}
	shares, err := money.Allocate(o.Discount, weights)
	if err != nil {
		return nil, fmt.Errorf("allocate discount: %w", err)
	}

	var subs []SubOrder
	index := map[SellerID]int{}
	for i, l := range o.Lines {
		l.Paid = l.Subtotal() - shares[i]
		n, ok := index[l.SellerID]
		if !ok {
			n = len(subs)
			index[l.SellerID] = n
			subs = append(subs, SubOrder{
				ID:       fmt.Sprintf("%s-%02d", o.ID, n+1),
				OrderID:  o.ID,
				SellerID: l.SellerID,
				Rate:     rates.Rate(l.SellerID),
			})
		}
		subs[n].Lines = append(subs[n].Lines, l)
		subs[n].Gross += l.Paid
	}

	for i := range subs {
		s := &subs[i]
		if !s.Rate.Valid() {
			return nil, fmt.Errorf("order: seller %s has invalid rate %d", s.SellerID, s.Rate)
		}
		s.Commission = s.Gross.ApplyRate(s.Rate)
		s.Net = s.Gross - s.Commission
	}
	return subs, nil
}
