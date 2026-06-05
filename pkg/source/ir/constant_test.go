package ir

import (
	"math"
	"testing"
)

// TestConstantString pins the canonical rendering of the millisecond-backed
// constants: UTC instants, largest-units-first durations, and the signed
// extremes — including the most negative int64, whose magnitude has no int64
// negation.
func TestConstantString(t *testing.T) {
	cases := []struct {
		c    *Constant
		want string
	}{
		{DatetimeConstant(0), "D1970-01-01T00:00:00.000Z"},
		{DatetimeConstant(-1000), "D1969-12-31T23:59:59.000Z"},
		{DurationConstant(0), "0ms"},
		{DurationConstant(90 * 60 * 1000), "1h30m"},
		{DurationConstant(-90 * 60 * 1000), "-1h30m"},
		{DurationConstant(3*604_800_000 + 4*86_400_000 + 5*3_600_000 + 6*60_000 + 7*1000 + 8), "3w4d5h6m7s8ms"},
		{DurationConstant(math.MaxInt64), "15250284452w3d7h12m55s807ms"},
		{DurationConstant(math.MinInt64), "-15250284452w3d7h12m55s808ms"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
