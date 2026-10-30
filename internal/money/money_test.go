package money

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func TestAllocate(t *testing.T) {
	tests := []struct {
		name    string
		total   Amount
		weights []int64
		want    []Amount
		err     error
	}{
		{"even split", 300, []int64{1, 1, 1}, []Amount{100, 100, 100}, nil},
		{"one unit left goes to first on tie", 100, []int64{1, 1, 1}, []Amount{34, 33, 33}, nil},
		{"two units left", 101, []int64{1, 1, 1}, []Amount{34, 34, 33}, nil},
		{"largest remainder wins over index", 10, []int64{1, 2, 3}, []Amount{2, 3, 5}, nil},
		{"proportional to prices", 1000, []int64{4999, 2999, 1999}, []Amount{500, 300, 200}, nil},
		{"zero weight gets nothing", 7, []int64{0, 1, 1}, []Amount{0, 4, 3}, nil},
		{"zero total", 0, []int64{0, 0}, []Amount{0, 0}, nil},
		{"single part", 12345, []int64{9}, []Amount{12345}, nil},
		{"huge values do not overflow", math.MaxInt64, []int64{math.MaxInt64, math.MaxInt64}, []Amount{math.MaxInt64/2 + 1, math.MaxInt64 / 2}, nil},
		{"no weights", 10, nil, nil, ErrNoWeights},
		{"negative weight", 10, []int64{1, -1}, nil, ErrNegativeWeight},
		{"all zero weights", 10, []int64{0, 0}, nil, ErrZeroWeights},
		{"negative total", -1, []int64{1}, nil, ErrNegativeAmount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Allocate(tt.total, tt.weights)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Allocate(%d, %v) = %v, want %v", tt.total, tt.weights, got, tt.want)
			}
		})
	}
}

func TestAllocateAlwaysSumsToTotal(t *testing.T) {
	weights := [][]int64{{1}, {3, 7}, {1, 1, 1, 1, 1, 1, 1}, {999, 1, 1}, {5, 0, 13, 2}}
	for _, w := range weights {
		for total := Amount(0); total < 500; total++ {
			parts, err := Allocate(total, w)
			if err != nil {
				t.Fatal(err)
			}
			if got := Sum(parts...); got != total {
				t.Fatalf("Allocate(%d, %v) sums to %d", total, w, got)
			}
		}
	}
}

func TestApplyRate(t *testing.T) {
	tests := []struct {
		amount Amount
		rate   BPS
		want   Amount
	}{
		{10000, 1000, 1000},
		{0, 1500, 0},
		{999, 0, 0},
		{999, 10000, 999},
		{1, 4999, 0},        // 0.4999 rounds down
		{1, 5000, 1},        // 0.5 rounds up
		{150, 1250, 19},     // 18.75
		{33333, 1500, 5000}, // 4999.95
		{12345, 333, 411},   // 411.0885
		{math.MaxInt64, 10000, math.MaxInt64},
	}
	for _, tt := range tests {
		if got := tt.amount.ApplyRate(tt.rate); got != tt.want {
			t.Errorf("%d.ApplyRate(%v) = %d, want %d", tt.amount, tt.rate, got, tt.want)
		}
	}
}

func TestMulDiv(t *testing.T) {
	if got := MulDiv(math.MaxInt64, 3, 4); got != math.MaxInt64/4*3+2 {
		t.Fatalf("MulDiv = %d", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on zero denominator")
		}
	}()
	MulDiv(1, 1, 0)
}
