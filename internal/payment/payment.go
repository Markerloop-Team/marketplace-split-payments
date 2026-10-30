// Package payment defines the boundary to a payment provider (card acquirer,
// bank QR scheme) and the events it sends back.
package payment

import (
	"context"
	"errors"
	"net/http"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

type Method string

const (
	MethodCard Method = "card"
	MethodQR   Method = "qr"
)

func (m Method) Valid() bool { return m == MethodCard || m == MethodQR }

var (
	ErrInvalidSignature = errors.New("payment: invalid webhook signature")
	ErrMalformedEvent   = errors.New("payment: malformed webhook")
	ErrRefundRejected   = errors.New("payment: refund rejected")
	ErrUnknownPayment   = errors.New("payment: unknown payment")
)

// CreateRequest starts a payment for the whole customer order. The provider
// sees a single amount; splitting between sellers is our business.
type CreateRequest struct {
	OrderID  string
	Amount   money.Amount
	Currency money.Currency
	Method   Method
	// IdempotencyKey makes retried requests return the original payment.
	IdempotencyKey string
}

// Payment is what the customer needs to complete payment: a hosted card page
// or a QR payload to scan in a banking app.
type Payment struct {
	ID          string
	Method      Method
	RedirectURL string
	QRPayload   string
}

type RefundRequest struct {
	PaymentID      string
	Amount         money.Amount
	IdempotencyKey string
}

type EventType string

const (
	EventCaptured EventType = "payment.captured"
	EventFailed   EventType = "payment.failed"
)

// Event is a verified webhook. ID is unique per provider and is what makes
// redelivery harmless.
type Event struct {
	ID        string
	Type      EventType
	PaymentID string
	OrderID   string
	Amount    money.Amount
	Currency  money.Currency
}

type Provider interface {
	Name() string
	CreatePayment(ctx context.Context, req CreateRequest) (Payment, error)
	Refund(ctx context.Context, req RefundRequest) error
	// ParseWebhook verifies the signature and decodes the event.
	ParseWebhook(h http.Header, body []byte) (Event, error)
}
