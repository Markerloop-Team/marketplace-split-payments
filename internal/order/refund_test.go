package order

import (
	"errors"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

// shoes: 3×999 + 1×500, books: 2×1 250, discount 101, 10% commission.
func refundFixture(t *testing.T) []SubOrder {
	t.Helper()
	o := newOrder(101,
		line("a", "shoes", 3, 999),
		line("b", "books", 2, 1250),
		line("c", "shoes", 1, 500),
	)
	subs, err := Split(o, FlatRates{Default: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return subs
}

func TestRefundOneSellersItems(t *testing.T) {
	subs := refundFixture(t)
	books := subs[1]

	parts, err := QuoteRefund(subs, []ReturnLine{{LineID: "b", Qty: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	p := parts[0]
	if p.SellerID != "books" || p.Amount != books.Gross || p.Commission != books.Commission || p.Net != books.Net {
		t.Fatalf("part = %+v, sub-order = %+v", p, books)
	}

	after := ApplyRefund(subs, parts)
	if after[0].Refunded != 0 || after[0].CommissionRefunded != 0 {
		t.Fatalf("other seller touched: %+v", after[0])
	}
	if after[1].Refunded != books.Gross || after[1].Lines[0].RefundedQty != 2 {
		t.Fatalf("books after refund: %+v", after[1])
	}
	if subs[1].Refunded != 0 {
		t.Fatal("ApplyRefund mutated its input")
	}
	if FullyRefunded(after) {
		t.Fatal("shoes are still kept")
	}
}

// Returning a line unit by unit must add up to exactly what was paid, and the
// commission reversed must add up to exactly what was charged.
func TestRefundInStepsHasNoRoundingDrift(t *testing.T) {
	tests := []struct {
		name  string
		steps [][]ReturnLine
	}{
		{"one unit at a time", [][]ReturnLine{
			{{"a", 1}}, {{"a", 1}}, {{"a", 1}}, {{"c", 1}}, {{"b", 1}}, {{"b", 1}},
		}},
		{"uneven chunks across sellers", [][]ReturnLine{
			{{"a", 2}, {"b", 1}}, {{"c", 1}}, {{"a", 1}, {"b", 1}},
		}},
		{"everything at once", [][]ReturnLine{
			{{"a", 3}, {"b", 2}, {"c", 1}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subs := refundFixture(t)
			original := subs
			var refunded money.Amount
			for _, step := range tt.steps {
				parts, err := QuoteRefund(subs, step)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range parts {
					if p.Net < 0 || p.Commission < 0 || p.Amount != p.Net+p.Commission {
						t.Fatalf("bad part %+v", p)
					}
					refunded += p.Amount
				}
				subs = ApplyRefund(subs, parts)
			}

			var gross money.Amount
			for i, s := range subs {
				gross += original[i].Gross
				if s.Refunded != original[i].Gross || s.CommissionRefunded != original[i].Commission {
					t.Errorf("%s: refunded %d/%d, commission %d/%d",
						s.SellerID, s.Refunded, original[i].Gross, s.CommissionRefunded, original[i].Commission)
				}
			}
			if refunded != gross {
				t.Errorf("refunded %d, customer paid %d", refunded, gross)
			}
			if !FullyRefunded(subs) {
				t.Error("expected fully refunded")
			}
		})
	}
}

func TestQuoteRefundRejects(t *testing.T) {
	subs := refundFixture(t)
	subs = ApplyRefund(subs, mustQuote(t, subs, ReturnLine{"a", 2}))

	tests := []struct {
		name    string
		returns []ReturnLine
		err     error
	}{
		{"more than left", []ReturnLine{{"a", 2}}, ErrInvalidReturn},
		{"zero qty", []ReturnLine{{"b", 0}}, ErrInvalidReturn},
		{"duplicate line", []ReturnLine{{"b", 1}, {"b", 1}}, ErrInvalidReturn},
		{"unknown line", []ReturnLine{{"zzz", 1}}, ErrUnknownLine},
		{"unknown among known", []ReturnLine{{"b", 1}, {"y", 1}, {"x", 1}}, ErrUnknownLine},
		{"empty", nil, ErrNothingToRefund},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := QuoteRefund(subs, tt.returns); !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
		})
	}
}

func mustQuote(t *testing.T, subs []SubOrder, r ...ReturnLine) []RefundPart {
	t.Helper()
	parts, err := QuoteRefund(subs, r)
	if err != nil {
		t.Fatal(err)
	}
	return parts
}
