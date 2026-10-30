package payout

import (
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/ledger"
)

func TestPlan(t *testing.T) {
	b := ledger.Balances{
		ledger.Settlement:              7600,
		ledger.CommissionRevenue:       -600,
		ledger.SellerPayable("big"):    -5000,
		ledger.SellerPayable("edge"):   -1000,
		ledger.SellerPayable("small"):  -999,
		ledger.SellerPayable("owes"):   250,
		ledger.SellerPayable("zero"):   0,
		ledger.SellerPayable("second"): -1251,
	}
	got := Plan(b, Policy{Minimum: 1000})

	want := []ledger.SellerBalance{{SellerID: "big", Amount: 5000}, {SellerID: "edge", Amount: 1000}, {SellerID: "second", Amount: 1251}}
	if len(got) != len(want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Plan = %v, want %v", got, want)
		}
	}
}
