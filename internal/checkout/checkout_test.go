package checkout_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
	"github.com/markerloop/marketplace-split-payments/internal/payment/fake"
	"github.com/markerloop/marketplace-split-payments/internal/storage/memory"
)

type fixture struct {
	svc      *checkout.Service
	store    *memory.Store
	provider *fake.Provider
}

func newFixture() fixture {
	store := memory.New()
	provider := fake.New("test-secret")
	rates := order.FlatRates{Default: 1000, Overrides: map[order.SellerID]money.BPS{"books": 500}}
	return fixture{svc: checkout.New(store, provider, rates), store: store, provider: provider}
}

// Three lines from two sellers with a 1 000 discount:
// shoes 2×12 000 + 1×5 000, books 1×9 000 → subtotal 38 000, total 37 000.
func (f fixture) placeOrder(t *testing.T) checkout.Checkout {
	t.Helper()
	co, err := f.svc.PlaceOrder(context.Background(), checkout.PlaceOrderInput{
		CustomerID: "cust-1",
		Currency:   money.KZT,
		Discount:   1000,
		Method:     payment.MethodQR,
		Lines: []checkout.LineInput{
			{SKU: "sneakers", SellerID: "shoes", Qty: 2, UnitPrice: 12000},
			{SKU: "novel", SellerID: "books", Qty: 1, UnitPrice: 9000},
			{SKU: "laces", SellerID: "shoes", Qty: 1, UnitPrice: 5000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return co
}

func (f fixture) capture(t *testing.T, co checkout.Checkout) {
	t.Helper()
	h, body, err := f.provider.Capture(co.Payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.HandleWebhook(context.Background(), h, body); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) assertBookBalanced(t *testing.T) {
	t.Helper()
	if total := f.store.Balances(money.KZT).Total(); total != 0 {
		t.Fatalf("ledger total = %d, want 0", total)
	}
}

func TestPlaceOrderSplitsBySeller(t *testing.T) {
	f := newFixture()
	co := f.placeOrder(t)

	if co.Order.Total() != 37000 || co.Order.Status != order.StatusPendingPayment {
		t.Fatalf("order = %+v", co.Order)
	}
	if len(co.SubOrders) != 2 {
		t.Fatalf("sub-orders = %d", len(co.SubOrders))
	}
	if co.Payment.QRPayload == "" {
		t.Fatal("expected QR payload")
	}
	if len(f.store.Entries()) != 0 {
		t.Fatal("nothing must be booked before capture")
	}
}

func TestPlaceOrderRejectsInvalidInput(t *testing.T) {
	f := newFixture()
	_, err := f.svc.PlaceOrder(context.Background(), checkout.PlaceOrderInput{
		Currency: money.KZT, Method: payment.MethodCard,
	})
	if !errors.Is(err, checkout.ErrInvalid) || !errors.Is(err, order.ErrEmptyOrder) {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureBooksLedger(t *testing.T) {
	f := newFixture()
	co := f.placeOrder(t)
	f.capture(t, co)

	o, _, err := f.svc.GetOrder(context.Background(), co.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != order.StatusPaid {
		t.Fatalf("status = %s", o.Status)
	}
	if n := len(f.store.Entries()); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
	f.assertBookBalanced(t)
}

func TestWebhookIsIdempotent(t *testing.T) {
	f := newFixture()
	co := f.placeOrder(t)
	h, body, err := f.provider.Capture(co.Payment.ID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.svc.HandleWebhook(context.Background(), h, body)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := len(f.store.Entries()); n != 1 {
		t.Fatalf("duplicate deliveries booked %d entries", n)
	}
	f.assertBookBalanced(t)
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	f := newFixture()
	co := f.placeOrder(t)
	h, body, _ := f.provider.Capture(co.Payment.ID)
	h.Set("X-Fakepay-Signature", "00")

	if err := f.svc.HandleWebhook(context.Background(), h, body); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Fatalf("err = %v", err)
	}
	if len(f.store.Entries()) != 0 {
		t.Fatal("forged webhook was booked")
	}
}

func TestDeclineThenCaptureKeepsOrderFailed(t *testing.T) {
	f := newFixture()
	co := f.placeOrder(t)
	ctx := context.Background()

	h, body, _ := f.provider.Decline(co.Payment.ID)
	if err := f.svc.HandleWebhook(ctx, h, body); err != nil {
		t.Fatal(err)
	}
	f.capture(t, co)

	o, _, _ := f.svc.GetOrder(ctx, co.Order.ID)
	if o.Status != order.StatusPaymentFailed {
		t.Fatalf("status = %s", o.Status)
	}
	if len(f.store.Entries()) != 0 {
		t.Fatal("late capture on a failed order was booked")
	}
}

func TestEventsLeaveThroughOutboxOnce(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	co := f.placeOrder(t)
	h, body, _ := f.provider.Capture(co.Payment.ID)
	for range 3 {
		if err := f.svc.HandleWebhook(ctx, h, body); err != nil {
			t.Fatal(err)
		}
	}

	pub := &outbox.MemoryPublisher{Err: errors.New("broker down")}
	relay := outbox.NewRelay(f.store, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := relay.Drain(ctx); err == nil {
		t.Fatal("expected publish error")
	}

	pub.Err = nil
	n, err := relay.Drain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("published %d messages, want 3 (order paid + 2 sub-orders)", n)
	}
	if n, _ := relay.Drain(ctx); n != 0 {
		t.Fatalf("second drain republished %d messages", n)
	}

	topics := map[string]int{}
	for _, m := range pub.Messages() {
		topics[m.Topic]++
	}
	if topics[checkout.TopicOrderPaid] != 1 || topics[checkout.TopicSubOrderCreated] != 2 {
		t.Fatalf("topics = %v", topics)
	}
}
