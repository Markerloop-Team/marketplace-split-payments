// Package money represents monetary values as integer minor units.
//
// Floats never touch money in this service: every amount is an int64 count of
// the currency's smallest unit (tiyn, cents), and every division states its
// rounding rule explicitly.
package money

import (
	"fmt"
	"math/bits"
)

// Currency is an ISO 4217 code.
type Currency string

const (
	KZT Currency = "KZT"
	UZS Currency = "UZS"
	USD Currency = "USD"
)

// Valid reports whether c looks like an ISO 4217 code.
func (c Currency) Valid() bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Amount is a quantity of minor units.
type Amount int64

// Sum adds amounts.
func Sum(amounts ...Amount) Amount {
	var s Amount
	for _, a := range amounts {
		s += a
	}
	return s
}

// MulDiv returns floor(a*num/den) for non-negative operands without
// overflowing the intermediate product. It panics if den is zero, any operand
// is negative, or the result does not fit into an Amount.
func MulDiv(a Amount, num, den int64) Amount {
	q, _ := mulDivRem(uint64(mustNonNeg(int64(a))), uint64(mustNonNeg(num)), uint64(mustNonNeg(den)))
	return q
}

func mulDivRem(a, num, den uint64) (Amount, uint64) {
	if den == 0 {
		panic("money: division by zero")
	}
	hi, lo := bits.Mul64(a, num)
	if hi >= den {
		panic("money: MulDiv overflow")
	}
	q, r := bits.Div64(hi, lo, den)
	if q > 1<<63-1 {
		panic("money: MulDiv overflow")
	}
	return Amount(q), r
}

func mustNonNeg(v int64) int64 {
	if v < 0 {
		panic(fmt.Sprintf("money: negative operand %d", v))
	}
	return v
}
