package api

import (
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

// All amounts on the wire are integers in minor units.

type placeOrderRequest struct {
	CustomerID string         `json:"customer_id"`
	Currency   money.Currency `json:"currency"`
	Discount   money.Amount   `json:"discount"`
	Method     payment.Method `json:"method"`
	Lines      []struct {
		SKU       string         `json:"sku"`
		SellerID  order.SellerID `json:"seller_id"`
		Qty       int64          `json:"qty"`
		UnitPrice money.Amount   `json:"unit_price"`
	} `json:"lines"`
}

type refundRequest struct {
	Lines []struct {
		LineID string `json:"line_id"`
		Qty    int64  `json:"qty"`
	} `json:"lines"`
}

type runPayoutsRequest struct {
	Currency money.Currency `json:"currency"`
	Minimum  money.Amount   `json:"minimum"`
}

type orderResponse struct {
	ID        string             `json:"id"`
	Status    order.Status       `json:"status"`
	Currency  money.Currency     `json:"currency"`
	Subtotal  money.Amount       `json:"subtotal"`
	Discount  money.Amount       `json:"discount"`
	Total     money.Amount       `json:"total"`
	PaymentID string             `json:"payment_id"`
	Payment   *paymentResponse   `json:"payment,omitempty"`
	SubOrders []subOrderResponse `json:"sub_orders"`
}

type paymentResponse struct {
	ID          string         `json:"id"`
	Method      payment.Method `json:"method"`
	RedirectURL string         `json:"redirect_url,omitempty"`
	QRPayload   string         `json:"qr_payload,omitempty"`
}

type subOrderResponse struct {
	ID         string         `json:"id"`
	SellerID   order.SellerID `json:"seller_id"`
	RateBps    money.BPS      `json:"rate_bps"`
	Gross      money.Amount   `json:"gross"`
	Commission money.Amount   `json:"commission"`
	Net        money.Amount   `json:"net"`
	Refunded   money.Amount   `json:"refunded"`
	Lines      []lineResponse `json:"lines"`
}

type lineResponse struct {
	ID          string       `json:"id"`
	SKU         string       `json:"sku"`
	Qty         int64        `json:"qty"`
	UnitPrice   money.Amount `json:"unit_price"`
	Paid        money.Amount `json:"paid"`
	RefundedQty int64        `json:"refunded_qty"`
}

type refundResponse struct {
	ID     string               `json:"id"`
	Amount money.Amount         `json:"amount"`
	Parts  []refundPartResponse `json:"parts"`
}

type refundPartResponse struct {
	SellerID   order.SellerID `json:"seller_id"`
	Amount     money.Amount   `json:"amount"`
	Commission money.Amount   `json:"commission"`
	Net        money.Amount   `json:"net"`
}

type payoutResponse struct {
	ID       string         `json:"id"`
	SellerID order.SellerID `json:"seller_id"`
	Amount   money.Amount   `json:"amount"`
	Currency money.Currency `json:"currency"`
	Status   payout.Status  `json:"status"`
}

func toOrderResponse(o order.Order, subs []order.SubOrder) orderResponse {
	resp := orderResponse{
		ID:        o.ID,
		Status:    o.Status,
		Currency:  o.Currency,
		Subtotal:  o.Subtotal(),
		Discount:  o.Discount,
		Total:     o.Total(),
		PaymentID: o.PaymentID,
		SubOrders: make([]subOrderResponse, 0, len(subs)),
	}
	for _, s := range subs {
		sr := subOrderResponse{
			ID: s.ID, SellerID: s.SellerID, RateBps: s.Rate,
			Gross: s.Gross, Commission: s.Commission, Net: s.Net, Refunded: s.Refunded,
		}
		for _, l := range s.Lines {
			sr.Lines = append(sr.Lines, lineResponse{
				ID: l.ID, SKU: l.SKU, Qty: l.Qty, UnitPrice: l.UnitPrice, Paid: l.Paid, RefundedQty: l.RefundedQty,
			})
		}
		resp.SubOrders = append(resp.SubOrders, sr)
	}
	return resp
}

func toRefundResponse(r order.Refund) refundResponse {
	resp := refundResponse{ID: r.ID, Amount: r.Amount}
	for _, p := range r.Parts {
		resp.Parts = append(resp.Parts, refundPartResponse{
			SellerID: p.SellerID, Amount: p.Amount, Commission: p.Commission, Net: p.Net,
		})
	}
	return resp
}
