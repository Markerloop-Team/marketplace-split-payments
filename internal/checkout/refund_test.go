package checkout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

func lineFor(t *testing.T, co checkout.Checkout, sku string) order.Line {
	t.Helper()
	for _, s := range co.SubOrders {
		for _, l := range s.Lines {
			if l.SKU == sku {
				return l
			}
		}
	}
	t.Fatalf("no line %s", sku)
	return order.Line{}
}

func TestRefundOneSellerLeavesOthersUntouched(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	co := f.placeOrder(t)
	f.capture(t, co)
	before := f.store.Balances(money.KZT)

	novel := lineFor(t, co, "novel")
	r, err := f.svc.Refund(ctx, checkout.RefundInput{
		OrderID:        co.Order.ID,
		IdempotencyKey: "rma-1",
		Lines:          []order.ReturnLine{{LineID: novel.ID, Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Amount != novel.Paid || len(r.Parts) != 1 || r.Parts[0].SellerID != "books" {
		t.Fatalf("refund = %+v", r)
	}

	after := f.store.Balances(money.KZT)
	if after[ledger.SellerPayable("shoes")] != before[ledger.SellerPayable("shoes")] {
		t.Error("shoes balance changed")
	}
	if after[ledger.SellerPayable("books")] != 0 || after[ledger.Settlement] != before[ledger.Settlement]-novel.Paid {
		t.Errorf("balances after refund: %v", after)
	}
	f.assertBookBalanced(t)

	o, subs, _ := f.svc.GetOrder(ctx, co.Order.ID)
	if o.Status != order.StatusPartiallyRefunded {
		t.Fatalf("status = %s", o.Status)
	}
	for _, s := range subs {
		if s.SellerID == "books" && s.Refunded != s.Gross {
			t.Fatalf("books sub-order refunded %d of %d", s.Refunded, s.Gross)
		}
	}
}

func TestRefundRetryWithSameKeyRefundsOnce(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	co := f.placeOrder(t)
	f.capture(t, co)

	in := checkout.RefundInput{
		OrderID:        co.Order.ID,
		IdempotencyKey: "rma-1",
		Lines:          []order.ReturnLine{{LineID: lineFor(t, co, "sneakers").ID, Qty: 1}},
	}
	first, err := f.svc.Refund(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Refund(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry created a new refund: %s vs %s", first.ID, second.ID)
	}
	if n := len(f.store.Entries()); n != 2 {
		t.Fatalf("entries = %d, want capture + one refund", n)
	}
}

func TestFullRefundAndValidation(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	co := f.placeOrder(t)

	_, err := f.svc.Refund(ctx, checkout.RefundInput{OrderID: co.Order.ID, IdempotencyKey: "k", Lines: []order.ReturnLine{{LineID: co.Order.Lines[0].ID, Qty: 1}}})
	if !errors.Is(err, checkout.ErrNotRefundable) {
		t.Fatalf("refund before capture: err = %v", err)
	}

	f.capture(t, co)
	var all []order.ReturnLine
	for _, l := range co.Order.Lines {
		all = append(all, order.ReturnLine{LineID: l.ID, Qty: l.Qty})
	}
	if _, err := f.svc.Refund(ctx, checkout.RefundInput{OrderID: co.Order.ID, IdempotencyKey: "all", Lines: all}); err != nil {
		t.Fatal(err)
	}
	o, _, _ := f.svc.GetOrder(ctx, co.Order.ID)
	if o.Status != order.StatusRefunded {
		t.Fatalf("status = %s", o.Status)
	}
	for acc, v := range f.store.Balances(money.KZT) {
		if v != 0 {
			t.Errorf("%s = %d after full refund", acc, v)
		}
	}

	_, err = f.svc.Refund(ctx, checkout.RefundInput{OrderID: co.Order.ID, IdempotencyKey: "again", Lines: all})
	if !errors.Is(err, checkout.ErrNotRefundable) {
		t.Fatalf("refund of a refunded order: err = %v", err)
	}
	if _, err := f.svc.Refund(ctx, checkout.RefundInput{OrderID: "nope", IdempotencyKey: "k"}); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatalf("unknown order: err = %v", err)
	}
}

// A refund after the seller was already paid leaves them owing the platform;
// the debt is recovered from their next sale before anything is paid out.
func TestRefundAfterPayoutIsNettedFromNextPayout(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	policy := payout.Policy{Minimum: 1}

	first := f.placeOrder(t)
	f.capture(t, first)
	if _, err := f.svc.RunPayouts(ctx, money.KZT, policy); err != nil {
		t.Fatal(err)
	}

	novel := lineFor(t, first, "novel")
	r, err := f.svc.Refund(ctx, checkout.RefundInput{
		OrderID: first.Order.ID, IdempotencyKey: "late", Lines: []order.ReturnLine{{LineID: novel.ID, Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owed := r.Parts[0].Net
	if got := f.store.Balances(money.KZT)[ledger.SellerPayable("books")]; got != owed {
		t.Fatalf("books balance = %d, want debt %d", got, owed)
	}
	if ps, _ := f.svc.RunPayouts(ctx, money.KZT, policy); len(ps) != 0 {
		t.Fatalf("paid out a seller in debt: %+v", ps)
	}

	second := f.placeOrder(t)
	f.capture(t, second)
	ps, err := f.svc.RunPayouts(ctx, money.KZT, policy)
	if err != nil {
		t.Fatal(err)
	}
	var booksNet money.Amount
	for _, s := range second.SubOrders {
		if s.SellerID == "books" {
			booksNet = s.Net
		}
	}
	for _, p := range ps {
		if p.SellerID == "books" && p.Amount != booksNet-owed {
			t.Fatalf("books payout = %d, want %d", p.Amount, booksNet-owed)
		}
	}
	f.assertBookBalanced(t)
}
