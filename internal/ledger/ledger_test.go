package ledger

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
)

func TestEntryValidate(t *testing.T) {
	ok := Entry{ID: "e", Ref: "r", Currency: money.KZT}
	tests := []struct {
		name     string
		postings []Posting
		err      error
	}{
		{"balanced", []Posting{Debit(Settlement, 100), Credit(CommissionRevenue, 100)}, nil},
		{"three legs", []Posting{Debit(Settlement, 100), Credit("a:x", 60), Credit("a:y", 40)}, nil},
		{"unbalanced", []Posting{Debit(Settlement, 100), Credit(CommissionRevenue, 99)}, ErrUnbalanced},
		{"single leg", []Posting{Debit(Settlement, 0)}, ErrTooFewLegs},
		{"zero leg", []Posting{Debit(Settlement, 0), Credit(CommissionRevenue, 0)}, ErrZeroPosting},
		{"empty account", []Posting{Debit("", 1), Credit(CommissionRevenue, 1)}, ErrMissingFields},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := ok
			e.Postings = tt.postings
			if err := e.Validate(); !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestCapture(t *testing.T) {
	o := order.Order{ID: "o1", Currency: money.KZT, Lines: []order.Line{
		{ID: "l1", SKU: "a", SellerID: "s1", Qty: 1, UnitPrice: 10000},
		{ID: "l2", SKU: "b", SellerID: "s2", Qty: 2, UnitPrice: 2500},
		{ID: "l3", SKU: "c", SellerID: "s3", Qty: 1, UnitPrice: 700},
	}}
	subs, err := order.Split(o, order.FlatRates{Default: 1000, Overrides: map[order.SellerID]money.BPS{"s3": 0}})
	if err != nil {
		t.Fatal(err)
	}

	e := Capture("e1", o, subs)
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}

	b := Balances{}
	b.Apply(e)
	want := Balances{
		Settlement:          15700,
		SellerPayable("s1"): -9000,
		SellerPayable("s2"): -4500,
		SellerPayable("s3"): -700,
		CommissionRevenue:   -1500,
	}
	for acc, v := range want {
		if b[acc] != v {
			t.Errorf("%s = %d, want %d", acc, b[acc], v)
		}
	}
	if len(b) != len(want) {
		t.Errorf("unexpected accounts: %v", b)
	}
}

func TestCaptureSkipsZeroCommission(t *testing.T) {
	o := order.Order{ID: "o1", Currency: money.KZT, Lines: []order.Line{
		{ID: "l1", SKU: "a", SellerID: "s1", Qty: 1, UnitPrice: 500},
	}}
	subs, _ := order.Split(o, order.FlatRates{})
	e := Capture("e1", o, subs)
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(e.Postings) != 2 {
		t.Fatalf("postings = %v", e.Postings)
	}
}

// Random carts with random discounts and rates must always produce balanced
// entries, and the book must stay at zero.
func TestCaptureAlwaysBalances(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	book := Balances{}
	for i := 0; i < 2000; i++ {
		o, rates := randomOrder(rng)
		subs, err := order.Split(o, rates)
		if err != nil {
			t.Fatal(err)
		}
		e := Capture("e", o, subs)
		if err := e.Validate(); err != nil {
			t.Fatalf("order %+v: %v", o, err)
		}
		book.Apply(e)
		if book.Total() != 0 {
			t.Fatalf("book total = %d after order %d", book.Total(), i)
		}
	}
}

func randomOrder(rng *rand.Rand) (order.Order, order.Rates) {
	sellers := []order.SellerID{"s1", "s2", "s3", "s4"}
	o := order.Order{ID: "o", Currency: money.KZT}
	for j := 0; j < 1+rng.Intn(6); j++ {
		o.Lines = append(o.Lines, order.Line{
			ID:        string(rune('a' + j)),
			SKU:       "sku",
			SellerID:  sellers[rng.Intn(len(sellers))],
			Qty:       int64(1 + rng.Intn(5)),
			UnitPrice: money.Amount(1 + rng.Intn(100000)),
		})
	}
	o.Discount = money.Amount(rng.Int63n(int64(o.Subtotal()) + 1))
	rates := order.FlatRates{Default: money.BPS(rng.Intn(3001))}
	return o, rates
}

func TestCaptureThenFullRefundZeroesEveryAccount(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		o, rates := randomOrder(rng)
		subs, err := order.Split(o, rates)
		if err != nil {
			t.Fatal(err)
		}
		if o.Total() == 0 {
			continue
		}
		var all []order.ReturnLine
		for _, l := range o.Lines {
			all = append(all, order.ReturnLine{LineID: l.ID, Qty: l.Qty})
		}
		parts, err := order.QuoteRefund(subs, all)
		if err != nil {
			t.Fatal(err)
		}
		r := order.Refund{ID: "r", Amount: o.Total(), Parts: parts}

		b := Balances{}
		for _, e := range []Entry{Capture("c", o, subs), Refund("r", r, o.Currency)} {
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			b.Apply(e)
		}
		for acc, v := range b {
			if v != 0 {
				t.Fatalf("order %d: %s = %d after full refund", i, acc, v)
			}
		}
	}
}
