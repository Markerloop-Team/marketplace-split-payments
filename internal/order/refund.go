package order

import (
	"errors"
	"fmt"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

var (
	ErrUnknownLine     = errors.New("order: unknown line")
	ErrInvalidReturn   = errors.New("order: invalid return quantity")
	ErrNothingToRefund = errors.New("order: nothing to refund")
)

// ReturnLine asks to return Qty units of a line.
type ReturnLine struct {
	LineID string
	Qty    int64
}

type Refund struct {
	ID             string
	OrderID        string
	IdempotencyKey string
	Amount         money.Amount
	Parts          []RefundPart
}

// RefundPart is one seller's share of a refund.
type RefundPart struct {
	SubOrderID string
	SellerID   SellerID
	Lines      []ReturnLine
	// Amount goes back to the customer.
	Amount money.Amount
	// Commission is given back by the platform.
	Commission money.Amount
	// Net is clawed back from the seller: Amount - Commission.
	Net money.Amount
}

// QuoteRefund prices returning some units of some lines.
//
// Amounts are computed from running totals rather than per unit: the refund
// for units a..b of a line is paid(b) - paid(a), where paid(q) is
// floor(Paid * q / Qty). Returning a line in any number of steps therefore
// adds up to exactly what the customer paid for it, and the same holds for
// the commission reversed on the sub-order. No unit is ever lost or gained
// to rounding.
func QuoteRefund(subs []SubOrder, returns []ReturnLine) ([]RefundPart, error) {
	want := make(map[string]int64, len(returns))
	for _, r := range returns {
		if r.Qty <= 0 {
			return nil, fmt.Errorf("%w: line %s qty %d", ErrInvalidReturn, r.LineID, r.Qty)
		}
		if _, dup := want[r.LineID]; dup {
			return nil, fmt.Errorf("%w: line %s listed twice", ErrInvalidReturn, r.LineID)
		}
		want[r.LineID] = r.Qty
	}

	var parts []RefundPart
	var total money.Amount
	for _, s := range subs {
		part := RefundPart{SubOrderID: s.ID, SellerID: s.SellerID}
		for _, l := range s.Lines {
			qty, ok := want[l.ID]
			if !ok {
				continue
			}
			delete(want, l.ID)
			if l.RefundedQty+qty > l.Qty {
				return nil, fmt.Errorf("%w: line %s has %d of %d left", ErrInvalidReturn, l.ID, l.Qty-l.RefundedQty, l.Qty)
			}
			part.Amount += paidFor(l, l.RefundedQty+qty) - paidFor(l, l.RefundedQty)
			part.Lines = append(part.Lines, ReturnLine{LineID: l.ID, Qty: qty})
		}
		if len(part.Lines) == 0 {
			continue
		}
		part.Commission = commissionFor(s, s.Refunded+part.Amount) - s.CommissionRefunded
		part.Net = part.Amount - part.Commission
		total += part.Amount
		parts = append(parts, part)
	}

	for id := range want {
		return nil, fmt.Errorf("%w: %s", ErrUnknownLine, id)
	}
	if total == 0 {
		return nil, ErrNothingToRefund
	}
	return parts, nil
}

// ApplyRefund returns copies of subs with the refund recorded.
func ApplyRefund(subs []SubOrder, parts []RefundPart) []SubOrder {
	out := make([]SubOrder, len(subs))
	for i, s := range subs {
		s.Lines = append([]Line(nil), s.Lines...)
		for _, p := range parts {
			if p.SubOrderID != s.ID {
				continue
			}
			s.Refunded += p.Amount
			s.CommissionRefunded += p.Commission
			for _, r := range p.Lines {
				for j := range s.Lines {
					if s.Lines[j].ID == r.LineID {
						s.Lines[j].RefundedQty += r.Qty
					}
				}
			}
		}
		out[i] = s
	}
	return out
}

// FullyRefunded reports whether every unit of every line has been returned.
func FullyRefunded(subs []SubOrder) bool {
	for _, s := range subs {
		for _, l := range s.Lines {
			if l.RefundedQty < l.Qty {
				return false
			}
		}
	}
	return true
}

func paidFor(l Line, qty int64) money.Amount {
	return money.MulDiv(l.Paid, qty, l.Qty)
}

func commissionFor(s SubOrder, refunded money.Amount) money.Amount {
	if s.Gross == 0 {
		return 0
	}
	return money.MulDiv(s.Commission, int64(refunded), int64(s.Gross))
}
