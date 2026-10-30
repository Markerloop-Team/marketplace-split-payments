package order

import (
	"errors"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/money"
)

func line(id string, seller SellerID, qty int64, price money.Amount) Line {
	return Line{ID: id, SKU: "sku-" + id, SellerID: seller, Qty: qty, UnitPrice: price}
}

func newOrder(discount money.Amount, lines ...Line) Order {
	return Order{ID: "ord1", CustomerID: "c1", Currency: money.KZT, Discount: discount, Lines: lines}
}

type want struct {
	seller     SellerID
	gross      money.Amount
	commission money.Amount
	net        money.Amount
}

func TestSplit(t *testing.T) {
	rates := FlatRates{Default: 1000, Overrides: map[SellerID]money.BPS{"books": 500, "free": 0}}

	tests := []struct {
		name  string
		order Order
		paid  []money.Amount // per line, in cart order
		want  []want
	}{
		{
			name:  "single seller no discount",
			order: newOrder(0, line("a", "shoes", 2, 15000)),
			paid:  []money.Amount{30000},
			want:  []want{{"shoes", 30000, 3000, 27000}},
		},
		{
			name: "sellers keep cart order and per-seller rates",
			order: newOrder(0,
				line("a", "shoes", 1, 10000),
				line("b", "books", 3, 2000),
				line("c", "shoes", 1, 5000),
			),
			paid: []money.Amount{10000, 6000, 5000},
			want: []want{{"shoes", 15000, 1500, 13500}, {"books", 6000, 300, 5700}},
		},
		{
			name: "discount remainder cents go to largest fractions",
			order: newOrder(100,
				line("a", "shoes", 1, 1000),
				line("b", "books", 1, 1000),
				line("c", "free", 1, 1000),
			),
			paid: []money.Amount{966, 967, 967},
			want: []want{{"shoes", 966, 97, 869}, {"books", 967, 48, 919}, {"free", 967, 0, 967}},
		},
		{
			name: "discount proportional to line subtotal",
			order: newOrder(999,
				line("a", "shoes", 1, 7000),
				line("b", "books", 2, 1500),
			),
			paid: []money.Amount{6301, 2700},
			want: []want{{"shoes", 6301, 630, 5671}, {"books", 2700, 135, 2565}},
		},
		{
			name:  "commission rounds half up once per sub-order",
			order: newOrder(0, line("a", "shoes", 3, 5), line("b", "shoes", 1, 0)),
			paid:  []money.Amount{15, 0},
			want:  []want{{"shoes", 15, 2, 13}},
		},
		{
			name:  "full discount",
			order: newOrder(500, line("a", "shoes", 1, 300), line("b", "books", 1, 200)),
			paid:  []money.Amount{0, 0},
			want:  []want{{"shoes", 0, 0, 0}, {"books", 0, 0, 0}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subs, err := Split(tt.order, rates)
			if err != nil {
				t.Fatal(err)
			}
			if len(subs) != len(tt.want) {
				t.Fatalf("got %d sub-orders, want %d", len(subs), len(tt.want))
			}

			var paid []money.Amount
			var gross money.Amount
			for i, s := range subs {
				w := tt.want[i]
				if s.SellerID != w.seller || s.Gross != w.gross || s.Commission != w.commission || s.Net != w.net {
					t.Errorf("sub %d = {%s %d %d %d}, want %v", i, s.SellerID, s.Gross, s.Commission, s.Net, w)
				}
				if s.Gross != s.Commission+s.Net {
					t.Errorf("sub %d: gross %d != commission %d + net %d", i, s.Gross, s.Commission, s.Net)
				}
				gross += s.Gross
				for _, l := range s.Lines {
					paid = append(paid, l.Paid)
				}
			}
			if gross != tt.order.Total() {
				t.Errorf("sum of gross %d != order total %d", gross, tt.order.Total())
			}
			assertSameMultiset(t, paid, tt.paid)
		})
	}
}

func assertSameMultiset(t *testing.T, got, want []money.Amount) {
	t.Helper()
	count := map[money.Amount]int{}
	for _, a := range want {
		count[a]++
	}
	for _, a := range got {
		count[a]--
	}
	for a, n := range count {
		if n != 0 {
			t.Fatalf("line amounts %v, want %v (mismatch at %d)", got, want, a)
		}
	}
}

func TestSplitRejectsInvalidOrders(t *testing.T) {
	rates := FlatRates{Default: 1000}
	tests := []struct {
		name  string
		order Order
		err   error
	}{
		{"no lines", newOrder(0), ErrEmptyOrder},
		{"zero qty", newOrder(0, line("a", "s", 0, 100)), ErrInvalidLine},
		{"negative price", newOrder(0, line("a", "s", 1, -1)), ErrInvalidLine},
		{"missing seller", newOrder(0, line("a", "", 1, 100)), ErrInvalidLine},
		{"discount above subtotal", newOrder(101, line("a", "s", 1, 100)), ErrDiscountTooBig},
		{"negative discount", newOrder(-1, line("a", "s", 1, 100)), ErrInvalidOrder},
		{"bad currency", Order{ID: "o", Currency: "kzt", Lines: []Line{line("a", "s", 1, 1)}}, ErrInvalidOrder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Split(tt.order, rates); !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestSplitRejectsInvalidRate(t *testing.T) {
	_, err := Split(newOrder(0, line("a", "s", 1, 100)), FlatRates{Default: 10001})
	if err == nil {
		t.Fatal("expected error for rate above 100%")
	}
}
