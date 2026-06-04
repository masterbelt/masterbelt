package types

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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
		{bt("int"), true, false},
		{bt("int8"), true, false},
		{bt("uint64"), true, false},
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
	for _, name := range []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "bool"} {
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
		{bt("int8"), bt("int"), true},   // both integer
		{bt("int8"), bt("int32"), true}, // both integer (range is checked elsewhere)
		{bt("bool"), bt("bool"), true},  // both boolean
		{bt("int8"), bt("bool"), false}, // kind mismatch
		{bt("bool"), bt("int8"), false}, //
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
		// Arithmetic unifies the operand types and returns self; the default
		// integer adapts to a sized operand.
		{"add int+int", bt("int"), "add", []ir.Type{bt("int")}, "int"},
		{"add concrete+int", bt("int32"), "add", []ir.Type{bt("int")}, "int32"},
		{"add int+concrete", bt("int"), "add", []ir.Type{bt("int32")}, "int32"},
		{"add same concrete", bt("int32"), "add", []ir.Type{bt("int32")}, "int32"},
		{"add mixed concrete", bt("int32"), "add", []ir.Type{bt("int8")}, "invalid"},
		{"add on bool", bt("bool"), "add", []ir.Type{bt("int32")}, "invalid"},
		{"add wrong arity", bt("int"), "add", nil, "invalid"},
		// Comparisons require integers and return bool.
		{"lt int", bt("int32"), "lt", []ir.Type{bt("int")}, "bool"},
		{"lt bool operand", bt("int32"), "lt", []ir.Type{bt("bool")}, "invalid"},
		// Equality is defined per kind; mixing kinds does not unify.
		{"eql int", bt("int"), "eql", []ir.Type{bt("int8")}, "bool"},
		{"eql bool", bt("bool"), "eql", []ir.Type{bt("bool")}, "bool"},
		{"eql mixed kinds", bt("int8"), "eql", []ir.Type{bt("bool")}, "invalid"},
		// Logical operators are defined on bool and return self.
		{"anan bool", bt("bool"), "anan", []ir.Type{bt("bool")}, "bool"},
		{"anan int", bt("int"), "anan", []ir.Type{bt("int")}, "invalid"},
		// Unary sign preserves an integer operand.
		{"neg int", bt("int8"), "neg", nil, "int8"},
		{"neg bool", bt("bool"), "neg", nil, "invalid"},
		{"neg with arg", bt("int8"), "neg", []ir.Type{bt("int8")}, "invalid"},
		// not requires a boolean and returns self.
		{"not bool", bt("bool"), "not", nil, "bool"},
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

// TestGenericMethodResult checks the generic method rule: a method on a generic
// application binds the receiver's type arguments (T = int for list<int>) and
// solves its own type variables (the R in map(func: fn(T): R): list<R>) by
// matching the parameter patterns against the argument types.
func TestGenericMethodResult(t *testing.T) {
	reg := builtin.Default()

	// A synthetic list<T> with len, the generic map, and a self-typed add — the
	// prelude is not available to this package, so the definition is built by hand.
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	listOf := func(arg ir.Type) ir.Type { return &ir.App{Def: listDef, Args: []ir.Type{arg}} }
	listDef.Methods = []*ir.Method{
		{Name: "len", Result: bt("int")},
		{Name: "map", Params: []ir.Param{{Name: "func", Type: &ir.Func{Params: []ir.Type{tvar("T")}, Result: tvar("R")}}}, Result: listOf(tvar("R"))},
		{Name: "add", Params: []ir.Param{{Name: "other", Type: &ir.SelfType{}}}, Result: &ir.SelfType{}},
	}
	fn := func(param, result ir.Type) ir.Type { return &ir.Func{Params: []ir.Type{param}, Result: result} }

	cases := []struct {
		name   string
		recv   ir.Type
		method string
		args   []ir.Type
		want   string
	}{
		// map binds T from the receiver and R from the function's result type.
		{"map int->int", listOf(bt("int")), "map", []ir.Type{fn(bt("int"), bt("int"))}, "list<int>"},
		{"map int->bool", listOf(bt("int")), "map", []ir.Type{fn(bt("int"), bt("bool"))}, "list<bool>"},
		// The function's parameter type must accept the element type.
		{"map wrong elem", listOf(bt("int")), "map", []ir.Type{fn(bt("int8"), bt("bool"))}, "invalid"},
		{"map non-function arg", listOf(bt("int")), "map", []ir.Type{bt("int")}, "invalid"},
		// A non-generic method on a generic receiver still resolves (App was not
		// understood before): len returns int, and the self-typed add returns the
		// receiver.
		{"len on list", listOf(bt("int")), "len", nil, "int"},
		{"add on list", listOf(bt("int")), "add", []ir.Type{listOf(bt("int"))}, "list<int>"},
	}
	for _, tc := range cases {
		if got := MethodResult(reg, tc.recv, tc.method, tc.args).String(); got != tc.want {
			t.Errorf("%s: MethodResult(%s, %q, ...) = %s, want %s", tc.name, tc.recv, tc.method, got, tc.want)
		}
	}
}

// TestNominalDerivation checks that a nominal type (type Level = int8) is
// integer-like, derives its underlying type's operator methods, and keeps its
// own identity in the result.
func TestNominalDerivation(t *testing.T) {
	reg := builtin.Default()
	level := &ir.Named{Def: &ir.TypeDef{
		Name: "Level",
		Body: bt("int8"),
		Methods: []*ir.Method{
			{Name: "increment", Result: &ir.SelfType{}},
		},
	}}

	if !IsInteger(reg, level) {
		t.Errorf("IsInteger(Level) = false, want true (underlying int8)")
	}

	// add is not declared on Level; it is derived from int8 and returns self,
	// which is Level. The default integer literal adapts to Level.
	if got := MethodResult(reg, level, "add", []ir.Type{bt("int")}).String(); got != "Level" {
		t.Errorf("MethodResult(Level, add, int) = %s, want Level", got)
	}
	// Level + Level is Level.
	if got := MethodResult(reg, level, "add", []ir.Type{level}).String(); got != "Level" {
		t.Errorf("MethodResult(Level, add, Level) = %s, want Level", got)
	}
	// A comparison derived from int8 returns bool.
	if got := MethodResult(reg, level, "lt", []ir.Type{bt("int")}).String(); got != "bool" {
		t.Errorf("MethodResult(Level, lt, int) = %s, want bool", got)
	}
	// Level's own method is found directly.
	if got := MethodResult(reg, level, "increment", nil).String(); got != "Level" {
		t.Errorf("MethodResult(Level, increment) = %s, want Level", got)
	}
	// Level does not implicitly convert to its underlying int8.
	if Assignable(reg, level, bt("int8")) {
		t.Errorf("Level should not be assignable to int8")
	}
	// The default integer adapts to Level.
	if !Assignable(reg, bt("int"), level) {
		t.Errorf("int should be assignable to the nominal integer Level")
	}
}
