package main

import (
	"math"
	"testing"
)

func TestClampSplitRatio(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, splitRatioDefault},
		{math.NaN(), splitRatioDefault},
		{0.05, splitRatioMin},
		{0.95, splitRatioMax},
		{-1, splitRatioMin},
		{0.35, 0.35},
	}
	for _, c := range cases {
		if got := clampSplitRatio(c.in); got != c.want {
			t.Errorf("clampSplitRatio(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
