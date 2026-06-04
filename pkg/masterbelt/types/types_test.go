package types

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

func TestDefault(t *testing.T) {
	cases := []struct{ in, want ir.Type }{
		{ir.UntypedInt, ir.Int64},
		{ir.UntypedBool, ir.Bool},
		{ir.Int32, ir.Int32}, // a concrete type is its own default
		{ir.Bool, ir.Bool},   //
		{ir.Invalid, ir.Invalid},
	}
	for _, tc := range cases {
		if got := Default(tc.in); got != tc.want {
			t.Errorf("Default(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestClassification(t *testing.T) {
	cases := []struct {
		t       ir.Type
		integer bool
		boolean bool
		untyped bool
	}{
		{ir.Invalid, false, false, false},
		{ir.UntypedInt, true, false, true},
		{ir.Int8, true, false, false},
		{ir.Int64, true, false, false},
		{ir.Uint64, true, false, false},
		{ir.UntypedBool, false, true, true},
		{ir.Bool, false, true, false},
	}
	for _, tc := range cases {
		if got := IsInteger(tc.t); got != tc.integer {
			t.Errorf("IsInteger(%s) = %v, want %v", tc.t, got, tc.integer)
		}
		if got := IsBoolean(tc.t); got != tc.boolean {
			t.Errorf("IsBoolean(%s) = %v, want %v", tc.t, got, tc.boolean)
		}
		if got := IsUntyped(tc.t); got != tc.untyped {
			t.Errorf("IsUntyped(%s) = %v, want %v", tc.t, got, tc.untyped)
		}
	}
}

func TestLookup(t *testing.T) {
	known := map[string]ir.Type{
		"int8": ir.Int8, "int16": ir.Int16, "int32": ir.Int32, "int64": ir.Int64,
		"uint8": ir.Uint8, "uint16": ir.Uint16, "uint32": ir.Uint32, "uint64": ir.Uint64,
		"bool": ir.Bool,
	}
	for name, want := range known {
		if got, ok := Lookup(name); !ok || got != want {
			t.Errorf("Lookup(%q) = (%s, %v), want (%s, true)", name, got, ok, want)
		}
	}
	// Names that are not concrete builtin types are not nameable.
	for _, name := range []string{"int", "Int8", "untyped int", "invalid", "notatype", ""} {
		if got, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q) = (%s, true), want not found", name, got)
		}
	}
}

func TestFits(t *testing.T) {
	n := func(s string) *big.Int {
		v, _ := new(big.Int).SetString(s, 10)
		return v
	}
	cases := []struct {
		t    ir.Type
		v    *big.Int
		want bool
	}{
		{ir.Int8, n("127"), true},
		{ir.Int8, n("128"), false},
		{ir.Int8, n("-128"), true},
		{ir.Int8, n("-129"), false},
		{ir.Uint8, n("0"), true},
		{ir.Uint8, n("255"), true},
		{ir.Uint8, n("256"), false},
		{ir.Uint8, n("-1"), false},
		{ir.Int64, n("9223372036854775807"), true},
		{ir.Int64, n("9223372036854775808"), false},
		// Types without a fixed range accept any value.
		{ir.UntypedInt, n("99999999999999999999999999"), true},
		{ir.Bool, n("5"), true},
		{ir.Invalid, n("5"), true},
	}
	for _, tc := range cases {
		if got := Fits(tc.t, tc.v); got != tc.want {
			t.Errorf("Fits(%s, %s) = %v, want %v", tc.t, tc.v, got, tc.want)
		}
	}
}

func TestCompatible(t *testing.T) {
	cases := []struct {
		annotation, expr ir.Type
		want             bool
	}{
		{ir.Int8, ir.UntypedInt, true},   // both integer
		{ir.Int8, ir.Int32, true},        // both integer (range is checked elsewhere)
		{ir.Bool, ir.UntypedBool, true},  // both boolean
		{ir.Int8, ir.Bool, false},        // kind mismatch
		{ir.UntypedBool, ir.Int8, false}, //
	}
	for _, tc := range cases {
		if got := Compatible(tc.annotation, tc.expr); got != tc.want {
			t.Errorf("Compatible(%s, %s) = %v, want %v", tc.annotation, tc.expr, got, tc.want)
		}
	}
}

func TestMethodResult(t *testing.T) {
	cases := []struct {
		name   string
		recv   ir.Type
		method string
		args   []ir.Type
		want   ir.Type
	}{
		// Arithmetic unifies the operand types.
		{"add untyped+untyped", ir.UntypedInt, "add", []ir.Type{ir.UntypedInt}, ir.UntypedInt},
		{"add concrete+untyped", ir.Int32, "add", []ir.Type{ir.UntypedInt}, ir.Int32},
		{"add untyped+concrete", ir.UntypedInt, "add", []ir.Type{ir.Int32}, ir.Int32},
		{"add same concrete", ir.Int32, "add", []ir.Type{ir.Int32}, ir.Int32},
		{"add mixed concrete", ir.Int32, "add", []ir.Type{ir.Int8}, ir.Invalid},
		{"add on bool", ir.Bool, "add", []ir.Type{ir.Int32}, ir.Invalid},
		{"add wrong arity", ir.UntypedInt, "add", nil, ir.Invalid},
		// Comparisons require integers and yield untyped bool.
		{"lt int", ir.Int32, "lt", []ir.Type{ir.UntypedInt}, ir.UntypedBool},
		{"lt bool operand", ir.Int32, "lt", []ir.Type{ir.Bool}, ir.Invalid},
		// Equality allows either two integers or two booleans.
		{"eql int", ir.UntypedInt, "eql", []ir.Type{ir.Int8}, ir.UntypedBool},
		{"eql bool", ir.Bool, "eql", []ir.Type{ir.UntypedBool}, ir.UntypedBool},
		{"eql mixed kinds", ir.Int8, "eql", []ir.Type{ir.Bool}, ir.Invalid},
		// Logical operators require booleans.
		{"anan bool", ir.UntypedBool, "anan", []ir.Type{ir.Bool}, ir.Bool},
		{"anan int", ir.UntypedInt, "anan", []ir.Type{ir.UntypedInt}, ir.Invalid},
		// Unary sign preserves an integer operand.
		{"neg int", ir.Int8, "neg", nil, ir.Int8},
		{"neg bool", ir.Bool, "neg", nil, ir.Invalid},
		{"neg with arg", ir.Int8, "neg", []ir.Type{ir.Int8}, ir.Invalid},
		// not requires a boolean.
		{"not bool", ir.UntypedBool, "not", nil, ir.UntypedBool},
		{"not int", ir.Int8, "not", nil, ir.Invalid},
		{"not with arg", ir.Bool, "not", []ir.Type{ir.Bool}, ir.Invalid},
		// Unknown methods do not apply to anything.
		{"unknown method", ir.Int8, "frobnicate", []ir.Type{ir.Int8}, ir.Invalid},
	}
	for _, tc := range cases {
		if got := MethodResult(tc.recv, tc.method, tc.args); got != tc.want {
			t.Errorf("%s: MethodResult(%s, %q, %v) = %s, want %s",
				tc.name, tc.recv, tc.method, tc.args, got, tc.want)
		}
	}
}
