package util

import "testing"

func TestKnownCostFormattingZeroNegativeAndSmallNumbers(t *testing.T) {
	for _, test := range []struct {
		value    float64
		variable bool
		want     string
	}{
		{0, false, "0.00"}, {0, true, "0.00"},
		{-10, false, "-10.00"}, {-10, true, "-10.000"},
		{-1000, false, "-1000.00"}, {-1000, true, "-1000.0"},
		{0.0001, true, "0.0001"}, {-0.0001, true, "-0.0001"},
		{123456789, false, "123456789.00"},
	} {
		if got := ConvertFloatToString(test.value, test.variable); got != test.want {
			t.Errorf("format(%g, %t) = %q, want %q", test.value, test.variable, got, test.want)
		}
	}
}
