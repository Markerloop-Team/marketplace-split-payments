package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
)

func create(t *testing.T, p *Provider, method payment.Method) payment.Payment {
	t.Helper()
	pay, err := p.CreatePayment(context.Background(), payment.CreateRequest{
		OrderID: "o1", Amount: 1000, Currency: money.KZT, Method: method, IdempotencyKey: "o1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return pay
}

func TestCreatePaymentIsIdempotent(t *testing.T) {
	p := New("s")
	first := create(t, p, payment.MethodQR)
	second := create(t, p, payment.MethodQR)
	if first.ID != second.ID {
		t.Fatalf("retry created a second payment: %s != %s", first.ID, second.ID)
	}
	if first.QRPayload == "" {
		t.Fatal("QR payment has no payload")
	}
}

func TestWebhookRoundTrip(t *testing.T) {
	p := New("s")
	pay := create(t, p, payment.MethodCard)
	h, body, err := p.Capture(pay.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := p.ParseWebhook(h, body)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventCaptured || ev.PaymentID != pay.ID || ev.OrderID != "o1" || ev.Amount != 1000 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestWebhookRejectsForgery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	p := New("s").WithClock(func() time.Time { return now })
	pay := create(t, p, payment.MethodCard)
	h, body, _ := p.Capture(pay.ID)

	tests := []struct {
		name   string
		mutate func() (*Provider, []byte)
	}{
		{"tampered body", func() (*Provider, []byte) {
			b := append([]byte{}, body...)
			b[len(b)-2] = 'X'
			return p, b
		}},
		{"wrong secret", func() (*Provider, []byte) {
			return New("other").WithClock(p.now), body
		}},
		{"replayed after skew window", func() (*Provider, []byte) {
			later := now.Add(maxClockSkew + time.Second)
			return New("s").WithClock(func() time.Time { return later }), body
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, b := tt.mutate()
			if _, err := verifier.ParseWebhook(h, b); !errors.Is(err, payment.ErrInvalidSignature) {
				t.Fatalf("err = %v, want invalid signature", err)
			}
		})
	}
}

func TestRefundLimits(t *testing.T) {
	ctx := context.Background()
	p := New("s")
	pay := create(t, p, payment.MethodCard)

	if err := p.Refund(ctx, payment.RefundRequest{PaymentID: pay.ID, Amount: 1, IdempotencyKey: "r0"}); !errors.Is(err, payment.ErrRefundRejected) {
		t.Fatalf("refund before capture: err = %v", err)
	}
	if _, _, err := p.Capture(pay.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Refund(ctx, payment.RefundRequest{PaymentID: pay.ID, Amount: 600, IdempotencyKey: "r1"}); err != nil {
		t.Fatal(err)
	}
	// Retrying the same refund must not count twice.
	if err := p.Refund(ctx, payment.RefundRequest{PaymentID: pay.ID, Amount: 600, IdempotencyKey: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Refund(ctx, payment.RefundRequest{PaymentID: pay.ID, Amount: 401, IdempotencyKey: "r2"}); !errors.Is(err, payment.ErrRefundRejected) {
		t.Fatalf("over-refund: err = %v", err)
	}
	if err := p.Refund(ctx, payment.RefundRequest{PaymentID: pay.ID, Amount: 400, IdempotencyKey: "r3"}); err != nil {
		t.Fatal(err)
	}
}
