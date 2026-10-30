// Package fake is an in-process payment provider that behaves like a typical
// card/QR acquirer: idempotent payment creation, HMAC-signed webhooks, and
// refunds capped at the captured amount.
package fake

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
)

const (
	headerSignature = "X-Fakepay-Signature"
	headerTimestamp = "X-Fakepay-Timestamp"
	maxClockSkew    = 5 * time.Minute
)

type Provider struct {
	secret []byte
	now    func() time.Time

	mu       sync.Mutex
	payments map[string]*record // by payment ID
	byKey    map[string]string  // idempotency key -> payment ID
	refunds  map[string]bool    // refund idempotency keys seen
}

type record struct {
	payment  payment.Payment
	req      payment.CreateRequest
	captured bool
	refunded money.Amount
}

func New(secret string) *Provider {
	return &Provider{
		secret:   []byte(secret),
		now:      time.Now,
		payments: map[string]*record{},
		byKey:    map[string]string{},
		refunds:  map[string]bool{},
	}
}

// WithClock replaces the clock used for webhook timestamps.
func (p *Provider) WithClock(now func() time.Time) *Provider {
	p.now = now
	return p
}

func (p *Provider) Name() string { return "fakepay" }

func (p *Provider) CreatePayment(_ context.Context, req payment.CreateRequest) (payment.Payment, error) {
	if req.Amount <= 0 || !req.Method.Valid() || req.IdempotencyKey == "" {
		return payment.Payment{}, fmt.Errorf("fakepay: invalid request")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if id, ok := p.byKey[req.IdempotencyKey]; ok {
		return p.payments[id].payment, nil
	}

	id := "pay_" + randomHex(8)
	pay := payment.Payment{ID: id, Method: req.Method}
	switch req.Method {
	case payment.MethodCard:
		pay.RedirectURL = "https://fakepay.invalid/checkout/" + id
	case payment.MethodQR:
		pay.QRPayload = fmt.Sprintf("FAKEPAY|%s|%d|%s", id, req.Amount, req.Currency)
	}
	p.payments[id] = &record{payment: pay, req: req}
	p.byKey[req.IdempotencyKey] = id
	return pay, nil
}

func (p *Provider) Refund(_ context.Context, req payment.RefundRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.refunds[req.IdempotencyKey] {
		return nil
	}
	r, ok := p.payments[req.PaymentID]
	if !ok {
		return payment.ErrUnknownPayment
	}
	if !r.captured || req.Amount <= 0 || r.refunded+req.Amount > r.req.Amount {
		return payment.ErrRefundRejected
	}
	r.refunded += req.Amount
	p.refunds[req.IdempotencyKey] = true
	return nil
}

type wireEvent struct {
	ID        string            `json:"id"`
	Type      payment.EventType `json:"type"`
	PaymentID string            `json:"payment_id"`
	OrderID   string            `json:"order_id"`
	Amount    int64             `json:"amount"`
	Currency  string            `json:"currency"`
}

// Capture simulates the customer completing payment and returns the signed
// webhook the provider would deliver.
func (p *Provider) Capture(paymentID string) (http.Header, []byte, error) {
	return p.settle(paymentID, payment.EventCaptured)
}

// Decline simulates a declined card or an expired QR code.
func (p *Provider) Decline(paymentID string) (http.Header, []byte, error) {
	return p.settle(paymentID, payment.EventFailed)
}

func (p *Provider) settle(paymentID string, typ payment.EventType) (http.Header, []byte, error) {
	p.mu.Lock()
	r, ok := p.payments[paymentID]
	if ok && typ == payment.EventCaptured {
		r.captured = true
	}
	p.mu.Unlock()
	if !ok {
		return nil, nil, payment.ErrUnknownPayment
	}

	body, err := json.Marshal(wireEvent{
		ID:        "evt_" + randomHex(8),
		Type:      typ,
		PaymentID: paymentID,
		OrderID:   r.req.OrderID,
		Amount:    int64(r.req.Amount),
		Currency:  string(r.req.Currency),
	})
	if err != nil {
		return nil, nil, err
	}
	return p.Sign(body), body, nil
}

// Sign returns headers carrying a signature over body.
func (p *Provider) Sign(body []byte) http.Header {
	ts := strconv.FormatInt(p.now().Unix(), 10)
	h := http.Header{}
	h.Set(headerTimestamp, ts)
	h.Set(headerSignature, p.mac(ts, body))
	return h
}

func (p *Provider) ParseWebhook(h http.Header, body []byte) (payment.Event, error) {
	ts := h.Get(headerTimestamp)
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return payment.Event{}, payment.ErrInvalidSignature
	}
	if d := p.now().Sub(time.Unix(sec, 0)); d > maxClockSkew || d < -maxClockSkew {
		return payment.Event{}, payment.ErrInvalidSignature
	}
	if !hmac.Equal([]byte(h.Get(headerSignature)), []byte(p.mac(ts, body))) {
		return payment.Event{}, payment.ErrInvalidSignature
	}

	var w wireEvent
	if err := json.Unmarshal(body, &w); err != nil || w.ID == "" || w.PaymentID == "" {
		return payment.Event{}, payment.ErrMalformedEvent
	}
	return payment.Event{
		ID:        w.ID,
		Type:      w.Type,
		PaymentID: w.PaymentID,
		OrderID:   w.OrderID,
		Amount:    money.Amount(w.Amount),
		Currency:  money.Currency(w.Currency),
	}, nil
}

func (p *Provider) mac(ts string, body []byte) string {
	m := hmac.New(sha256.New, p.secret)
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
