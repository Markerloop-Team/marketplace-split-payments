package money

import "fmt"

// BPS is a rate in basis points: 100 bps = 1%.
type BPS int64

const bpsDenominator = 10_000

// Valid reports whether r is within 0%..100%.
func (r BPS) Valid() bool { return r >= 0 && r <= bpsDenominator }

func (r BPS) String() string { return fmt.Sprintf("%d.%02d%%", r/100, r%100) }

// ApplyRate returns a*r rounded half up to the nearest minor unit.
// Half-up is what sellers expect to see on their statements; banker's rounding
// would make identical lines differ by a unit and generate support tickets.
func (a Amount) ApplyRate(r BPS) Amount {
	if !r.Valid() {
		panic(fmt.Sprintf("money: rate out of range: %d", r))
	}
	q, rem := mulDivRem(uint64(mustNonNeg(int64(a))), uint64(r), bpsDenominator)
	if rem*2 >= bpsDenominator {
		q++
	}
	return q
}
