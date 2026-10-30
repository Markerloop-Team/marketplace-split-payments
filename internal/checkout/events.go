package checkout

import (
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
)

const (
	TopicOrderPaid          = "orders.paid"
	TopicOrderPaymentFailed = "orders.payment_failed"
	// TopicSubOrderCreated tells a seller to start fulfilment.
	TopicSubOrderCreated = "suborders.created"
)

type OrderEvent struct {
	OrderID    string         `json:"order_id"`
	CustomerID string         `json:"customer_id"`
	PaymentID  string         `json:"payment_id"`
	Total      money.Amount   `json:"total"`
	Currency   money.Currency `json:"currency"`
}

type SubOrderEvent struct {
	SubOrderID string         `json:"sub_order_id"`
	OrderID    string         `json:"order_id"`
	SellerID   order.SellerID `json:"seller_id"`
	Gross      money.Amount   `json:"gross"`
	Commission money.Amount   `json:"commission"`
	Net        money.Amount   `json:"net"`
	Currency   money.Currency `json:"currency"`
	Lines      []EventLine    `json:"lines"`
}

type EventLine struct {
	LineID string       `json:"line_id"`
	SKU    string       `json:"sku"`
	Qty    int64        `json:"qty"`
	Paid   money.Amount `json:"paid"`
}

func orderEvent(topic string, o order.Order) (outbox.Message, error) {
	return outbox.New(topic, o.ID, OrderEvent{
		OrderID:    o.ID,
		CustomerID: o.CustomerID,
		PaymentID:  o.PaymentID,
		Total:      o.Total(),
		Currency:   o.Currency,
	})
}

func subOrderEvents(o order.Order, subs []order.SubOrder) ([]outbox.Message, error) {
	msgs := make([]outbox.Message, 0, len(subs))
	for _, s := range subs {
		ev := SubOrderEvent{
			SubOrderID: s.ID,
			OrderID:    o.ID,
			SellerID:   s.SellerID,
			Gross:      s.Gross,
			Commission: s.Commission,
			Net:        s.Net,
			Currency:   o.Currency,
		}
		for _, l := range s.Lines {
			ev.Lines = append(ev.Lines, EventLine{LineID: l.ID, SKU: l.SKU, Qty: l.Qty, Paid: l.Paid})
		}
		m, err := outbox.New(TopicSubOrderCreated, string(s.SellerID), ev)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}
