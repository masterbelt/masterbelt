// This file mirrors generic.go: substitution, structural matching (over unions
// and records), and interface-bound satisfaction.
package types

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestSubstituteAndMatch covers the exported substitution and pattern-match
// rules directly: Substitute pins bound variables through composite types, and
// Match solves a pattern's variables against a concrete argument.
func TestSubstituteAndMatch(t *testing.T) {
	reg := builtin.Default()
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	fn := func(param, result ir.Type) ir.Type { return &ir.Func{Params: []ir.Type{param}, Result: result} }

	// Substitute reaches into a function type and leaves unbound variables.
	subst := map[string]ir.Type{"T": bt("nint")}
	if got := Substitute(fn(tvar("T"), tvar("R")), subst).String(); got != "fn(nint): R" {
		t.Errorf("Substitute = %s, want fn(nint): R", got)
	}
	// An empty substitution returns the type unchanged.
	pattern := fn(tvar("T"), tvar("R"))
	if got := Substitute(pattern, map[string]ir.Type{}); got != pattern {
		t.Errorf("Substitute with no bindings = %v, want the original", got)
	}

	// Match binds the pattern's variables structurally...
	subst = map[string]ir.Type{}
	if !Match(reg, fn(tvar("T"), tvar("R")), fn(bt("nint"), bt("bool")), subst) {
		t.Fatal("Match(fn(T): R, fn(nint): bool) failed")
	}
	if subst["T"].String() != "nint" || subst["R"].String() != "bool" {
		t.Errorf("subst = %v, want T=nint R=bool", subst)
	}
	// ...requires an already-bound variable to agree...
	if Match(reg, tvar("T"), bt("bool"), subst) {
		t.Error("Match rebound T=nint to bool")
	}
	// ...and falls back to assignability for concrete patterns, so the
	// default int adapts.
	if !Match(reg, bt("sbyte"), bt("nint"), map[string]ir.Type{}) {
		t.Error("Match(sbyte, nint) must allow the default-nint adaption")
	}
	if Match(reg, bt("sbyte"), bt("bool"), map[string]ir.Type{}) {
		t.Error("Match(sbyte, bool) must fail")
	}
}

// TestMatchUnionAndRecord checks Match recurses into a union or record pattern
// and solves the type variables nested in it, the way Substitute and
// FreeTypeVars already reach into them — so a generic helper whose parameter is
// `T | error` or `{ v: T }` solves T from its argument.
func TestMatchUnionAndRecord(t *testing.T) {
	reg := builtin.Default()
	t.Run("union", func(t *testing.T) { matchUnionCases(t, reg) })
	t.Run("record", func(t *testing.T) { matchRecordCases(t, reg) })
}

// matchUnionCases checks Match against union patterns: a value binding the
// variable member, a concrete member preferred when no variable member solves
// it, two same-arity unions pairing positionally, and a concrete union keeping
// the old assignability rule (member value, reordered, narrower).
func matchUnionCases(t *testing.T, reg *builtin.Registry) {
	t.Helper()
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	union := func(ms ...ir.Type) ir.Type { return &ir.Union{Members: ms} }

	// A value flowing into a `T | error` pattern binds T to the value's type.
	subst := map[string]ir.Type{}
	if !Match(reg, union(tvar("T"), bt("error")), bt("nint"), subst) {
		t.Fatal("Match(T | error, nint) failed")
	}
	if got := subst["T"]; got == nil || got.String() != "nint" {
		t.Errorf("subst[T] = %v, want nint", got)
	}

	// The concrete member is preferred only when no variable member solves it:
	// an error argument matches the error member without binding T.
	subst = map[string]ir.Type{}
	if !Match(reg, union(tvar("T"), bt("error")), bt("error"), subst) {
		t.Fatal("Match(T | error, error) failed")
	}
	if _, bound := subst["T"]; bound {
		t.Errorf("error should match the error member, leaving T unbound; subst = %v", subst)
	}

	// Two unions of the same arity pair positionally.
	subst = map[string]ir.Type{}
	if !Match(reg, union(tvar("T"), bt("error")), union(bt("nint"), bt("error")), subst) {
		t.Fatal("Match(T | error, nint | error) failed")
	}
	if got := subst["T"]; got == nil || got.String() != "nint" {
		t.Errorf("subst[T] = %v, want nint", got)
	}

	// A concrete union pattern (no variable) keeps the old assignability rule,
	// which accepts a member value, a reordered union, and a narrower union —
	// the recursion must not narrow the non-generic path.
	if !Match(reg, union(bt("nint"), bt("error")), bt("error"), map[string]ir.Type{}) {
		t.Error("Match(nint | error, error) must accept the member value")
	}
	if !Match(reg, union(bt("nint"), bt("error")), union(bt("error"), bt("nint")), map[string]ir.Type{}) {
		t.Error("Match(nint | error, error | nint) must accept the reordered union")
	}
	if !Match(reg, union(bt("nint"), bt("error"), bt("string")), union(bt("nint"), bt("error")), map[string]ir.Type{}) {
		t.Error("Match(nint | error | string, nint | error) must accept the narrower union")
	}
}

// matchRecordCases checks Match against record patterns: { v: T } solving T from
// the argument's same-named field (through a nominal record), and the failures —
// a missing field or a non-record argument.
func matchRecordCases(t *testing.T, reg *builtin.Registry) {
	t.Helper()
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	record := func(fs ...ir.Field) ir.Type { return &ir.Record{Fields: fs} }
	field := func(name string, t ir.Type) ir.Field { return ir.Field{Name: name, Type: t} }

	// A record pattern { v: T } solves T from the argument's same-named field,
	// looking through a nominal record.
	box := &ir.Named{Def: &ir.TypeDef{Name: "Box", Body: record(field("v", bt("nint")))}}
	subst := map[string]ir.Type{}
	if !Match(reg, record(field("v", tvar("T"))), box, subst) {
		t.Fatal("Match({ v: T }, Box{ v: nint }) failed")
	}
	if got := subst["T"]; got == nil || got.String() != "nint" {
		t.Errorf("subst[T] = %v, want nint", got)
	}

	// A field the argument lacks (or a non-record argument) does not match.
	subst = map[string]ir.Type{}
	if Match(reg, record(field("missing", tvar("T"))), box, subst) {
		t.Error("Match({ missing: T }, Box) must fail")
	}
	if Match(reg, record(field("v", tvar("T"))), bt("nint"), map[string]ir.Type{}) {
		t.Error("Match({ v: T }, nint) must fail (nint is no record)")
	}
}

// TestSatisfies checks the nominal-satisfaction rule a generic-function bound
// uses: a type satisfies an interface bound only when it opts into the
// interface (an entry in its Impls) with matching arguments, and a bounded type
// parameter resolves its bound interface's methods (defOf/receiverSubst).
func TestSatisfies(t *testing.T) {
	reg := builtin.Default()

	// An interface foldable<K, V> with one method fold(): V, and a Bag that opts
	// into foldable<int, int>.
	foldable := &ir.TypeDef{
		Name:      "foldable",
		Interface: &ir.InterfaceDef{Required: []string{"fold"}},
		Params:    []*ir.TypeParam{{Name: "K"}, {Name: "V"}},
		Methods: []*ir.Method{
			{Name: "fold", Result: &ir.TypeVar{Name: "V"}},
		},
	}
	bound := &ir.App{Def: foldable, Args: []ir.Type{bt("nint"), bt("nint")}}
	bag := &ir.TypeDef{Name: "Bag", Body: bt("nint"), Impls: []ir.Type{bound}}

	if !Satisfies(reg, &ir.Named{Def: bag}, bound) {
		t.Error("Satisfies(Bag, foldable<nint, nint>) = false, want true (Bag opts in)")
	}
	// A type with no impl does not satisfy.
	plain := &ir.TypeDef{Name: "Plain", Body: bt("nint")}
	if Satisfies(reg, &ir.Named{Def: plain}, bound) {
		t.Error("Satisfies(Plain, foldable<nint, nint>) = true, want false (no impl)")
	}
	// A bare builtin does not satisfy a bound it never opts into.
	if Satisfies(reg, bt("nint"), bound) {
		t.Error("Satisfies(nint, foldable<nint, nint>) = true, want false")
	}
	// A non-interface bound never satisfies.
	if Satisfies(reg, &ir.Named{Def: bag}, bt("nint")) {
		t.Error("Satisfies(Bag, nint) = true, want false (nint is not an interface)")
	}

	// A bounded type parameter resolves its bound interface's methods: a value
	// typed T where T: foldable<int, int> can call fold, whose V reads as int.
	tvarBounded := &ir.TypeVar{Name: "T", Bound: bound}
	if got := MethodResult(reg, tvarBounded, "fold", nil).String(); got != "nint" {
		t.Errorf("MethodResult(T: foldable<nint, nint>, fold) = %s, want nint", got)
	}
	// An unbounded type parameter has no methods.
	tvarBare := &ir.TypeVar{Name: "T"}
	if got := MethodResult(reg, tvarBare, "fold", nil); got != ir.Invalid {
		t.Errorf("MethodResult(unbounded T, fold) = %s, want invalid", got)
	}
}
