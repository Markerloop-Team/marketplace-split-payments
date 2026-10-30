// Package checkout wires the order, payment and ledger domains into use cases.
package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
)

var (
	ErrInvalid         = errors.New("invalid request")
	ErrPaymentMismatch = errors.New("payment does not match order")
)

type Service struct {
	store    Store
	provider payment.Provider
	rates    order.Rates
	newID    func() string
}

func New(store Store, provider payment.Provider, rates order.Rates) *Service {
	return &Service{store: store, provider: provider, rates: rates, newID: uuid.NewString}
}

type LineInput struct {
	SKU       string
	SellerID  order.SellerID
	Qty       int64
	UnitPrice money.Amount
}

type PlaceOrderInput struct {
	CustomerID string
	Currency   money.Currency
	Discount   money.Amount
	Method     payment.Method
	Lines      []LineInput
}

type Checkout struct {
	Order     order.Order
	SubOrders []order.SubOrder
	Payment   payment.Payment
}

// PlaceOrder splits the cart, opens a single payment for the order total and
// stores the order awaiting payment.
//
// The payment is created before the order is stored: if the insert fails we
// leave an unpaid intent at the provider, which expires on its own, instead of
// an order nobody can pay.
func (s *Service) PlaceOrder(ctx context.Context, in PlaceOrderInput) (Checkout, error) {
	if !in.Method.Valid() {
		return Checkout{}, fmt.Errorf("%w: unknown payment method %q", ErrInvalid, in.Method)
	}

	o := order.Order{
		ID:         "ord_" + s.newID(),
		CustomerID: in.CustomerID,
		Currency:   in.Currency,
		Discount:   in.Discount,
		Status:     order.StatusPendingPayment,
	}
	for i, l := range in.Lines {
		o.Lines = append(o.Lines, order.Line{
			ID:        fmt.Sprintf("%s-L%d", o.ID, i+1),
			SKU:       l.SKU,
			SellerID:  l.SellerID,
			Qty:       l.Qty,
			UnitPrice: l.UnitPrice,
		})
	}

	subs, err := order.Split(o, s.rates)
	if err != nil {
		return Checkout{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if o.Total() == 0 {
		return Checkout{}, fmt.Errorf("%w: nothing to pay", ErrInvalid)
	}

	pay, err := s.provider.CreatePayment(ctx, payment.CreateRequest{
		OrderID:        o.ID,
		Amount:         o.Total(),
		Currency:       o.Currency,
		Method:         in.Method,
		IdempotencyKey: o.ID,
	})
	if err != nil {
		return Checkout{}, fmt.Errorf("create payment: %w", err)
	}
	o.PaymentID = pay.ID

	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		return tx.InsertOrder(ctx, o, subs)
	})
	if err != nil {
		return Checkout{}, fmt.Errorf("store order: %w", err)
	}
	return Checkout{Order: o, SubOrders: subs, Payment: pay}, nil
}

func (s *Service) GetOrder(ctx context.Context, id string) (order.Order, []order.SubOrder, error) {
	var (
		o    order.Order
		subs []order.SubOrder
	)
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		o, subs, err = tx.LoadOrder(ctx, id)
		return err
	})
	return o, subs, err
}

func (s *Service) postEntry(ctx context.Context, tx Tx, e ledger.Entry) error {
	if err := e.Validate(); err != nil {
		return fmt.Errorf("entry %s: %w", e.Ref, err)
	}
	return tx.PostEntry(ctx, e)
}
