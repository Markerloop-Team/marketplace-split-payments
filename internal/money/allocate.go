package money

import (
	"errors"
	"sort"
)

var (
	ErrNoWeights      = errors.New("money: no weights")
	ErrNegativeWeight = errors.New("money: negative weight")
	ErrZeroWeights    = errors.New("money: all weights are zero")
	ErrNegativeAmount = errors.New("money: negative amount")
)

// Allocate splits total across weights proportionally, so that the parts
// always sum to exactly total.
//
// Each part first gets the floor of its exact share. The units left over
// (fewer than len(weights)) go to the parts with the largest fractional
// remainder; ties go to the lower index, which keeps the result
// deterministic for identical inputs.
func Allocate(total Amount, weights []int64) ([]Amount, error) {
	if len(weights) == 0 {
		return nil, ErrNoWeights
	}
	if total < 0 {
		return nil, ErrNegativeAmount
	}

	var sum uint64
	for _, w := range weights {
		if w < 0 {
			return nil, ErrNegativeWeight
		}
		sum += uint64(w)
	}

	parts := make([]Amount, len(weights))
	if total == 0 {
		return parts, nil
	}
	if sum == 0 {
		return nil, ErrZeroWeights
	}

	rems := make([]uint64, len(weights))
	allocated := Amount(0)
	for i, w := range weights {
		parts[i], rems[i] = mulDivRem(uint64(total), uint64(w), sum)
		allocated += parts[i]
	}

	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]] > rems[order[b]] })

	for i := 0; allocated < total; i++ {
		parts[order[i]]++
		allocated++
	}
	return parts, nil
}
