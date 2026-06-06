package ir

import (
	"math"
	"math/big"
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

// TestEnumConstant pins the enum value form: its rendering (Type.Member) and
// the name/value accessors over the member table.
func TestEnumConstant(t *testing.T) {
	def := &TypeDef{
		Name: "Rarity",
		Enum: &EnumDef{
			Base: "uint8",
			Members: []EnumMember{
				{Name: "Common", Value: IntConstant(big.NewInt(1))},
				{Name: "Legend", Value: IntConstant(big.NewInt(10))},
			},
		},
	}
	legend := EnumConstant(def, 1)
	if got := legend.String(); got != "Rarity.Legend" {
		t.Errorf("String() = %q, want Rarity.Legend", got)
	}
	if got := legend.EnumName(); got != "Legend" {
		t.Errorf("EnumName() = %q, want Legend", got)
	}
	if v := legend.EnumValue(); v == nil || v.String() != "10" {
		t.Errorf("EnumValue() = %v, want 10", v)
	}
	// An out-of-range index has no name or value, rather than panicking.
	bad := EnumConstant(def, 9)
	if bad.EnumName() != "" || bad.EnumValue() != nil {
		t.Errorf("out-of-range member: want empty name and nil value, got %q / %v", bad.EnumName(), bad.EnumValue())
	}
}

// TestNullConstant pins the null value: it renders as "null" (the String default
// would dereference a nil Int and panic without an explicit case), and two null
// values are equal — the single-inhabitant rule ConstantsEqual relies on so the
// folder and the early-cutoff agree.
func TestNullConstant(t *testing.T) {
	n := NullConstant()
	if got := n.String(); got != "null" {
		t.Errorf("String() = %q, want null", got)
	}
	if !ConstantsEqual(n, NullConstant()) {
		t.Error("two null constants should be equal")
	}
	if ConstantsEqual(n, IntConstant(big.NewInt(0))) {
		t.Error("null must not equal an integer")
	}
}

// TestRangeConstant pins the range value: it renders as range(start, end) (the
// String default would dereference a nil Int and panic without an explicit
// case), equality is by the bounds, and a range never equals a value of another
// kind — the contract ConstantsEqual's panic guard forces the new kind to honor.
func TestRangeConstant(t *testing.T) {
	r := RangeConstant(big.NewInt(0), big.NewInt(10))
	if got := r.String(); got != "range(0, 10)" {
		t.Errorf("String() = %q, want range(0, 10)", got)
	}
	if !ConstantsEqual(r, RangeConstant(big.NewInt(0), big.NewInt(10))) {
		t.Error("two ranges with the same bounds should be equal")
	}
	if ConstantsEqual(r, RangeConstant(big.NewInt(0), big.NewInt(9))) {
		t.Error("ranges with different bounds must not be equal")
	}
	// Differing kinds are never equal — a range against the integer it counts to.
	if ConstantsEqual(r, IntConstant(big.NewInt(10))) {
		t.Error("a range must not equal an integer")
	}
}
