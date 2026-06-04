package types

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// bt is shorthand for a builtin type by name.
func bt(name string) ir.Type { return &ir.Builtin{Name: name} }

func TestDefault(t *testing.T) {
	cases := []struct {
		in   ir.Type
		want string
	}{
		{ir.UntypedInt, "int64"},
		{ir.UntypedBool, "bool"},
		{bt("int32"), "int32"}, // a concrete type is its own default
		{ir.Invalid, "invalid"},
	}
	for _, tc := range cases {
		if got := Default(tc.in).String(); got != tc.want {
			t.Errorf("Default(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestClassification(t *testing.T) {
	reg := builtin.Default()
	cases := []struct {
		t       ir.Type
		integer bool
		boolean bool
		untyped bool
	}{
		{ir.Invalid, false, false, false},
		{ir.UntypedInt, true, false, true},
		{bt("int8"), true, false, false},
		{bt("uint64"), true, false, false},
		{ir.UntypedBool, false, true, true},
		{bt("bool"), false, true, false},
	}
	for _, tc := range cases {
		if got := IsInteger(reg, tc.t); got != tc.integer {
			t.Errorf("IsInteger(%s) = %v, want %v", tc.t, got, tc.integer)
		}
		if got := IsBoolean(reg, tc.t); got != tc.boolean {
			t.Errorf("IsBoolean(%s) = %v, want %v", tc.t, got, tc.boolean)
		}
		if got := IsUntyped(tc.t); got != tc.untyped {
			t.Errorf("IsUntyped(%s) = %v, want %v", tc.t, got, tc.untyped)
		}
	}
}

func TestLookup(t *testing.T) {
	reg := builtin.Default()
	for _, name := range []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "bool"} {
		if got, ok := Lookup(reg, name); !ok || got.String() != name {
			t.Errorf("Lookup(%q) = (%s, %v), want (%s, true)", name, got, ok, name)
		}
	}
	// Names that are not registered builtins are not nameable.
	for _, name := range []string{"Int8", "untyped int", "invalid", "notatype", ""} {
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
		{bt("int8"), n("127"), true},
		{bt("int8"), n("128"), false},
		{bt("int8"), n("-128"), true},
		{bt("int8"), n("-129"), false},
		{bt("uint8"), n("0"), true},
		{bt("uint8"), n("255"), true},
		{bt("uint8"), n("256"), false},
		{bt("uint8"), n("-1"), false},
		{bt("int64"), n("9223372036854775807"), true},
		{bt("int64"), n("9223372036854775808"), false},
		// Arbitrary-precision and non-integer types accept any value.
		{bt("int"), n("99999999999999999999999999"), true},
		{ir.UntypedInt, n("99999999999999999999999999"), true},
		{bt("bool"), n("5"), true},
		{ir.Invalid, n("5"), true},
		// An unsigned arbitrary integer still rejects negatives.
		{bt("uint"), n("-1"), false},
	}
	for _, tc := range cases {
		if got := Fits(reg, tc.t, tc.v); got != tc.want {
			t.Errorf("Fits(%s, %s) = %v, want %v", tc.t, tc.v, got, tc.want)
		}
	}
}

func TestCompatible(t *testing.T) {
	reg := builtin.Default()
	cases := []struct {
		annotation, expr ir.Type
		want             bool
	}{
		{bt("int8"), ir.UntypedInt, true},   // both integer
		{bt("int8"), bt("int32"), true},     // both integer (range is checked elsewhere)
		{bt("bool"), ir.UntypedBool, true},  // both boolean
		{bt("int8"), bt("bool"), false},     // kind mismatch
		{ir.UntypedBool, bt("int8"), false}, //
	}
	for _, tc := range cases {
		if got := Compatible(reg, tc.annotation, tc.expr); got != tc.want {
			t.Errorf("Compatible(%s, %s) = %v, want %v", tc.annotation, tc.expr, got, tc.want)
		}
	}
}

func TestMethodResult(t *testing.T) {
	reg := builtin.Default()
	cases := []struct {
		name   string
		recv   ir.Type
		method string
		args   []ir.Type
		want   string
	}{
		// Arithmetic unifies the operand types and returns self.
		{"add untyped+untyped", ir.UntypedInt, "add", []ir.Type{ir.UntypedInt}, "untyped int"},
		{"add concrete+untyped", bt("int32"), "add", []ir.Type{ir.UntypedInt}, "int32"},
		{"add untyped+concrete", ir.UntypedInt, "add", []ir.Type{bt("int32")}, "int32"},
		{"add same concrete", bt("int32"), "add", []ir.Type{bt("int32")}, "int32"},
		{"add mixed concrete", bt("int32"), "add", []ir.Type{bt("int8")}, "invalid"},
		{"add on bool", bt("bool"), "add", []ir.Type{bt("int32")}, "invalid"},
		{"add wrong arity", ir.UntypedInt, "add", nil, "invalid"},
		// Comparisons require integers and return the concrete bool of their signature.
		{"lt int", bt("int32"), "lt", []ir.Type{ir.UntypedInt}, "bool"},
		{"lt bool operand", bt("int32"), "lt", []ir.Type{bt("bool")}, "invalid"},
		// Equality is defined per kind; mixing kinds does not unify.
		{"eql int", ir.UntypedInt, "eql", []ir.Type{bt("int8")}, "bool"},
		{"eql bool", bt("bool"), "eql", []ir.Type{ir.UntypedBool}, "bool"},
		{"eql mixed kinds", bt("int8"), "eql", []ir.Type{bt("bool")}, "invalid"},
		// Logical operators return self, so two untyped bools stay untyped.
		{"anan untyped", ir.UntypedBool, "anan", []ir.Type{ir.UntypedBool}, "untyped bool"},
		{"anan concrete", bt("bool"), "anan", []ir.Type{ir.UntypedBool}, "bool"},
		{"anan int", ir.UntypedInt, "anan", []ir.Type{ir.UntypedInt}, "invalid"},
		// Unary sign preserves an integer operand.
		{"neg int", bt("int8"), "neg", nil, "int8"},
		{"neg bool", bt("bool"), "neg", nil, "invalid"},
		{"neg with arg", bt("int8"), "neg", []ir.Type{bt("int8")}, "invalid"},
		// not requires a boolean and returns self.
		{"not untyped", ir.UntypedBool, "not", nil, "untyped bool"},
		{"not int", bt("int8"), "not", nil, "invalid"},
		{"not with arg", bt("bool"), "not", []ir.Type{bt("bool")}, "invalid"},
		// Unknown methods do not apply to anything.
		{"unknown method", bt("int8"), "frobnicate", []ir.Type{bt("int8")}, "invalid"},
	}
	for _, tc := range cases {
		if got := MethodResult(reg, tc.recv, tc.method, tc.args).String(); got != tc.want {
			t.Errorf("%s: MethodResult(%s, %q, ...) = %s, want %s", tc.name, tc.recv, tc.method, got, tc.want)
		}
	}
}
