// Package order models a multi-seller customer order and its split into
// per-seller sub-orders.
package order

import (
	"errors"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

type SellerID string

type Status string

const (
	StatusPendingPayment    Status = "pending_payment"
	StatusPaid              Status = "paid"
	StatusPaymentFailed     Status = "payment_failed"
	StatusPartiallyRefunded Status = "partially_refunded"
	StatusRefunded          Status = "refunded"
)

var (
	ErrEmptyOrder     = errors.New("order: no lines")
	ErrInvalidLine    = errors.New("order: invalid line")
	ErrInvalidOrder   = errors.New("order: invalid order")
	ErrDiscountTooBig = errors.New("order: discount exceeds subtotal")
)

// Line is one SKU in the cart.
type Line struct {
	ID        string
	SKU       string
	SellerID  SellerID
	Qty       int64
	UnitPrice money.Amount
	// Paid is what the customer actually paid for the line after its share of
	// the order discount. Set by Split.
	Paid        money.Amount
	RefundedQty int64
}

func (l Line) Subtotal() money.Amount { return l.UnitPrice * money.Amount(l.Qty) }

// Order is what the customer checks out: one cart, one payment, many sellers.
type Order struct {
	ID         string
	CustomerID string
	Currency   money.Currency
	Lines      []Line
	// Discount is an order-level promo, funded by sellers pro rata.
	Discount  money.Amount
	Status    Status
	PaymentID string
}

func (o Order) Subtotal() money.Amount {
	var s money.Amount
	for _, l := range o.Lines {
		s += l.Subtotal()
	}
	return s
}

func (o Order) Total() money.Amount { return o.Subtotal() - o.Discount }

func (o Order) Validate() error {
	if o.ID == "" || !o.Currency.Valid() || o.Discount < 0 {
		return ErrInvalidOrder
	}
	if len(o.Lines) == 0 {
		return ErrEmptyOrder
	}
	for _, l := range o.Lines {
		if l.ID == "" || l.SellerID == "" || l.SKU == "" || l.Qty <= 0 || l.UnitPrice < 0 {
			return ErrInvalidLine
		}
	}
	if o.Discount > o.Subtotal() {
		return ErrDiscountTooBig
	}
	return nil
}
