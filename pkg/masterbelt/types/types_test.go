package types

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// bt is shorthand for a builtin type by name.
func bt(name string) ir.Type { return &ir.Builtin{Name: name} }

func TestClassification(t *testing.T) {
	reg := builtin.Default()
	cases := []struct {
		t       ir.Type
		integer bool
		boolean bool
	}{
		{ir.Invalid, false, false},
		{bt("nint"), true, false},
		{bt("sbyte"), true, false},
		{bt("ulong"), true, false},
		{bt("bool"), false, true},
	}
	for _, tc := range cases {
		if got := IsInteger(reg, tc.t); got != tc.integer {
			t.Errorf("IsInteger(%s) = %v, want %v", tc.t, got, tc.integer)
		}
		if got := IsBoolean(reg, tc.t); got != tc.boolean {
			t.Errorf("IsBoolean(%s) = %v, want %v", tc.t, got, tc.boolean)
		}
	}
}

func TestLookup(t *testing.T) {
	reg := builtin.Default()
	for _, name := range []string{"nint", "sbyte", "short", "int", "long", "nuint", "byte", "ushort", "uint", "ulong", "bool"} {
		if got, ok := Lookup(reg, name); !ok || got.String() != name {
			t.Errorf("Lookup(%q) = (%s, %v), want (%s, true)", name, got, ok, name)
		}
	}
	// Names that are not registered builtins are not nameable.
	for _, name := range []string{"Int8", "invalid", "notatype", ""} {
		if got, ok := Lookup(reg, name); ok {
			t.Errorf("Lookup(%q) = (%s, true), want not found", name, got)
		}
	}
}

func TestFits(t *testing.T) {
	reg := builtin.Default()
	n := func(s string) *big.Int {
		v, _ := new(big.Int).SetString(s, 10)
		return v
	}
	cases := []struct {
		t    ir.Type
		v    *big.Int
		want bool
	}{
		{bt("sbyte"), n("127"), true},
		{bt("sbyte"), n("128"), false},
		{bt("sbyte"), n("-128"), true},
		{bt("sbyte"), n("-129"), false},
		{bt("byte"), n("0"), true},
		{bt("byte"), n("255"), true},
		{bt("byte"), n("256"), false},
		{bt("byte"), n("-1"), false},
		{bt("long"), n("9223372036854775807"), true},
		{bt("long"), n("9223372036854775808"), false},
		// Arbitrary-precision and non-integer types accept any value.
		{bt("nint"), n("99999999999999999999999999"), true},
		{bt("bool"), n("5"), true},
		{ir.Invalid, n("5"), true},
		// An unsigned arbitrary integer still rejects negatives.
		{bt("nuint"), n("-1"), false},
	}
	for _, tc := range cases {
		if got := Fits(reg, tc.t, tc.v); got != tc.want {
			t.Errorf("Fits(%s, %s) = %v, want %v", tc.t, tc.v, got, tc.want)
		}
	}
}
