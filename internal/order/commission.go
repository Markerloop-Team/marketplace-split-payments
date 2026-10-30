package order

import "github.com/markerloop/marketplace-split-payments/internal/money"

// Rates returns the commission rate charged to a seller.
type Rates interface {
	Rate(SellerID) money.BPS
}

// FlatRates charges Default to everyone except sellers listed in Overrides.
type FlatRates struct {
	Default   money.BPS
	Overrides map[SellerID]money.BPS
}

func (r FlatRates) Rate(id SellerID) money.BPS {
	if bps, ok := r.Overrides[id]; ok {
		return bps
	}
	return r.Default
}
