// This file mirrors method.go: the method-result rule (MethodResult, generic
// and index forms), receiver binding, overload selection, and nominal-type
// method derivation.
package types

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

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
		{"add nint+nint", bt("nint"), "add", []ir.Type{bt("nint")}, "nint"},
		{"add concrete+nint", bt("int"), "add", []ir.Type{bt("nint")}, "int"},
		{"add nint+concrete", bt("nint"), "add", []ir.Type{bt("int")}, "int"},
		{"add same concrete", bt("int"), "add", []ir.Type{bt("int")}, "int"},
		{"add mixed concrete", bt("int"), "add", []ir.Type{bt("sbyte")}, "invalid"},
		{"add on bool", bt("bool"), "add", []ir.Type{bt("int")}, "invalid"},
		{"add wrong arity", bt("nint"), "add", nil, "invalid"},
		// Comparisons require integers and return bool.
		{"lt nint", bt("int"), "lt", []ir.Type{bt("nint")}, "bool"},
		{"lt bool operand", bt("int"), "lt", []ir.Type{bt("bool")}, "invalid"},
		// Equality is defined per kind; mixing kinds does not unify.
		{"eql nint", bt("nint"), "eql", []ir.Type{bt("sbyte")}, "bool"},
		{"eql bool", bt("bool"), "eql", []ir.Type{bt("bool")}, "bool"},
		{"eql mixed kinds", bt("sbyte"), "eql", []ir.Type{bt("bool")}, "invalid"},
		// Logical operators are defined on bool and return self.
		{"anan bool", bt("bool"), "anan", []ir.Type{bt("bool")}, "bool"},
		{"anan nint", bt("nint"), "anan", []ir.Type{bt("nint")}, "invalid"},
		// Unary sign preserves an integer operand.
		{"neg nint", bt("sbyte"), "neg", nil, "sbyte"},
		{"neg bool", bt("bool"), "neg", nil, "invalid"},
		{"neg with arg", bt("sbyte"), "neg", []ir.Type{bt("sbyte")}, "invalid"},
		// not requires a boolean and returns self.
		{"not bool", bt("bool"), "not", nil, "bool"},
		{"not nint", bt("sbyte"), "not", nil, "invalid"},
		{"not with arg", bt("bool"), "not", []ir.Type{bt("bool")}, "invalid"},
		// Unknown methods do not apply to anything.
		{"unknown method", bt("sbyte"), "frobnicate", []ir.Type{bt("sbyte")}, "invalid"},
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
		{Name: "len", Result: bt("nint")},
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
		{"map nint->nint", listOf(bt("nint")), "map", []ir.Type{fn(bt("nint"), bt("nint"))}, "list<nint>"},
		{"map nint->bool", listOf(bt("nint")), "map", []ir.Type{fn(bt("nint"), bt("bool"))}, "list<bool>"},
		// The function's parameter type must accept the element type.
		{"map wrong elem", listOf(bt("nint")), "map", []ir.Type{fn(bt("sbyte"), bt("bool"))}, "invalid"},
		{"map non-function arg", listOf(bt("nint")), "map", []ir.Type{bt("nint")}, "invalid"},
		// A non-generic method on a generic receiver still resolves (App was not
		// understood before): len returns int, and the self-typed add returns the
		// receiver.
		{"len on list", listOf(bt("nint")), "len", nil, "nint"},
		{"add on list", listOf(bt("nint")), "add", []ir.Type{listOf(bt("nint"))}, "list<nint>"},
	}
	for _, tc := range cases {
		if got := MethodResult(reg, tc.recv, tc.method, tc.args).String(); got != tc.want {
			t.Errorf("%s: MethodResult(%s, %q, ...) = %s, want %s", tc.name, tc.recv, tc.method, got, tc.want)
		}
	}
}

// TestIndexMethodResult checks that the index methods' result types substitute
// the receiver's type variable inside a union and through self: get's T | error
// becomes int | error on a list<int> (the element variable is bound under the
// union member), and set's self becomes the receiver type. This is the rule the
// desugared coll[i] / coll[i] = v rely on — a subscript is just a method call.
func TestIndexMethodResult(t *testing.T) {
	reg := builtin.Default()

	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	union := func(members ...ir.Type) ir.Type { return &ir.Union{Members: members} }
	errType := bt("error")

	// A synthetic list<T> and map<K, V> carrying the prelude's index methods, built
	// by hand (the prelude is not available to this package).
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	listDef.Methods = []*ir.Method{
		{Name: "get", Params: []ir.Param{{Name: "index", Type: bt("nint")}}, Result: union(tvar("T"), errType)},
		{Name: "set", Params: []ir.Param{{Name: "index", Type: bt("nint")}, {Name: "value", Type: tvar("T")}}, Result: &ir.SelfType{}},
	}
	listOf := func(arg ir.Type) ir.Type { return &ir.App{Def: listDef, Args: []ir.Type{arg}} }

	mapDef := &ir.TypeDef{Name: "map", Builtin: true, Params: []*ir.TypeParam{{Name: "K"}, {Name: "V"}}}
	mapDef.Methods = []*ir.Method{
		{Name: "get", Params: []ir.Param{{Name: "key", Type: tvar("K")}}, Result: union(tvar("V"), errType)},
		{Name: "set", Params: []ir.Param{{Name: "key", Type: tvar("K")}, {Name: "value", Type: tvar("V")}}, Result: &ir.SelfType{}},
	}
	mapOf := func(k, v ir.Type) ir.Type { return &ir.App{Def: mapDef, Args: []ir.Type{k, v}} }

	cases := []struct {
		name   string
		recv   ir.Type
		method string
		args   []ir.Type
		want   string
	}{
		// get substitutes the element variable inside the union: T | error becomes
		// int | error on a list<int>, V | error becomes int | error on a map.
		{"list get", listOf(bt("nint")), "get", []ir.Type{bt("nint")}, "nint | error"},
		{"map get", mapOf(bt("string"), bt("nint")), "get", []ir.Type{bt("string")}, "nint | error"},
		// set returns self — the receiver type, with its arguments bound.
		{"list set", listOf(bt("nint")), "set", []ir.Type{bt("nint"), bt("nint")}, "list<nint>"},
		{"map set", mapOf(bt("string"), bt("nint")), "set", []ir.Type{bt("string"), bt("nint")}, "map<string, nint>"},
	}
	for _, tc := range cases {
		if got := MethodResult(reg, tc.recv, tc.method, tc.args).String(); got != tc.want {
			t.Errorf("%s: MethodResult(%s, %q, ...) = %s, want %s", tc.name, tc.recv, tc.method, got, tc.want)
		}
	}
}

// TestBindReceiver checks the carved-out first half of the method rule: the
// lookup and the receiver-argument bindings, with the per-method variables
// left for the caller to solve.
func TestBindReceiver(t *testing.T) {
	reg := builtin.Default()
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	listDef.Methods = []*ir.Method{
		{Name: "map", Params: []ir.Param{{Name: "func", Type: &ir.Func{Params: []ir.Type{&ir.TypeVar{Name: "T"}}, Result: &ir.TypeVar{Name: "R"}}}}, Result: &ir.App{Def: listDef, Args: []ir.Type{&ir.TypeVar{Name: "R"}}}},
	}
	recv := &ir.App{Def: listDef, Args: []ir.Type{bt("nint")}}

	m, subst, ok := BindReceiver(reg, recv, "map")
	if !ok || m == nil || m.Name != "map" {
		t.Fatalf("BindReceiver(list<nint>, map) = %v, %v, %v", m, subst, ok)
	}
	// T is bound from the receiver; R stays unbound for Match to solve.
	if got := subst["T"]; got == nil || got.String() != "nint" {
		t.Errorf("subst[T] = %v, want nint", got)
	}
	if _, bound := subst["R"]; bound {
		t.Errorf("R must stay unbound, got %v", subst["R"])
	}

	if _, _, ok := BindReceiver(reg, recv, "frobnicate"); ok {
		t.Error("BindReceiver found a method that does not exist")
	}
	if _, _, ok := BindReceiver(reg, ir.Invalid, "map"); ok {
		t.Error("BindReceiver resolved a method on the invalid type")
	}
}

// overloadedScore builds a nominal type with an overloaded method — the merge
// of the 0013-overload example: merge(points: self): self and
// merge(active: bool): bool — for the selection tests.
func overloadedScore() *ir.Named {
	return &ir.Named{Def: &ir.TypeDef{
		Name: "Score",
		Body: bt("int"),
		Methods: []*ir.Method{
			{Name: "merge", Params: []ir.Param{{Name: "points", Type: &ir.SelfType{}}}, Result: &ir.SelfType{}},
			{Name: "merge", Params: []ir.Param{{Name: "active", Type: bt("bool")}}, Result: bt("bool")},
		},
	}}
}

// TestSelectOverload checks the overload selection rule: the candidates are
// filtered by arity and argument fit, and the call resolves exactly when one
// survives.
func TestSelectOverload(t *testing.T) {
	reg := builtin.Default()
	score := overloadedScore()

	// A self-typed argument picks the points signature; the default integer
	// adapts to the receiver.
	matches, found := SelectOverload(reg, score, "merge", []ir.Type{bt("nint")})
	if !found || len(matches) != 1 {
		t.Fatalf("merge(nint): matches = %d, found = %v, want 1, true", len(matches), found)
	}
	if got := matches[0].Operand.String(); got != "Score" {
		t.Errorf("merge(nint): operand = %s, want Score", got)
	}

	// A boolean argument picks the flag signature.
	matches, _ = SelectOverload(reg, score, "merge", []ir.Type{bt("bool")})
	if len(matches) != 1 || matches[0].Method.Result.String() != "bool" {
		t.Fatalf("merge(bool): matches = %d, want the bool overload", len(matches))
	}

	// A string fits neither overload: found, but no match.
	matches, found = SelectOverload(reg, score, "merge", []ir.Type{bt("string")})
	if !found || len(matches) != 0 {
		t.Errorf("merge(string): matches = %d, found = %v, want 0, true", len(matches), found)
	}

	// An unknown method is not found at all — the caller distinguishes the
	// missing method from the unmatched overload.
	if _, found := SelectOverload(reg, score, "frobnicate", nil); found {
		t.Error("frobnicate: found = true, want false")
	}

	// Arity selects among same-name signatures: next() vs next(steps: self).
	level := &ir.Named{Def: &ir.TypeDef{
		Name: "Level",
		Body: bt("sbyte"),
		Methods: []*ir.Method{
			{Name: "next", Result: &ir.SelfType{}},
			{Name: "next", Params: []ir.Param{{Name: "steps", Type: &ir.SelfType{}}}, Result: &ir.SelfType{}},
		},
	}}
	if matches, _ := SelectOverload(reg, level, "next", nil); len(matches) != 1 || len(matches[0].Method.Params) != 0 {
		t.Errorf("next(): want the zero-parameter overload")
	}
	if matches, _ := SelectOverload(reg, level, "next", []ir.Type{bt("nint")}); len(matches) != 1 || len(matches[0].Method.Params) != 1 {
		t.Errorf("next(nint): want the stepping overload")
	}

	// The default integer fits both sized-integer overloads at once: ambiguous,
	// resolved by an annotation at the call site, never an implicit priority.
	gauge := &ir.Named{Def: &ir.TypeDef{
		Name: "Gauge",
		Body: bt("int"),
		Methods: []*ir.Method{
			{Name: "set", Params: []ir.Param{{Name: "v", Type: bt("sbyte")}}, Result: bt("bool")},
			{Name: "set", Params: []ir.Param{{Name: "v", Type: bt("short")}}, Result: bt("bool")},
		},
	}}
	if matches, _ := SelectOverload(reg, gauge, "set", []ir.Type{bt("nint")}); len(matches) != 2 {
		t.Errorf("set(nint): matches = %d, want 2 (ambiguous)", len(matches))
	}
	// A sized argument is exact: unambiguous.
	if matches, _ := SelectOverload(reg, gauge, "set", []ir.Type{bt("short")}); len(matches) != 1 || matches[0].Method.Params[0].Type.String() != "short" {
		t.Errorf("set(short): want the short overload alone")
	}

	// A nil argument type (a function literal checked after selection) fits
	// any parameter, so only the arity discriminates.
	if matches, _ := SelectOverload(reg, score, "merge", []ir.Type{nil}); len(matches) != 2 {
		t.Errorf("merge(<literal>): matches = %d, want 2", len(matches))
	}

	// A single-candidate method behaves as before: the generic map solves its
	// variables from the argument, and each selection works on a fresh
	// substitution.
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	listDef.Methods = []*ir.Method{
		{Name: "map", Params: []ir.Param{{Name: "func", Type: &ir.Func{Params: []ir.Type{tvar("T")}, Result: tvar("R")}}}, Result: &ir.App{Def: listDef, Args: []ir.Type{tvar("R")}}},
	}
	recv := &ir.App{Def: listDef, Args: []ir.Type{bt("nint")}}
	fn := &ir.Func{Params: []ir.Type{bt("nint")}, Result: bt("bool")}
	matches, _ = SelectOverload(reg, recv, "map", []ir.Type{fn})
	if len(matches) != 1 {
		t.Fatalf("map(fn): matches = %d, want 1", len(matches))
	}
	if got := matches[0].Subst["R"]; got == nil || got.String() != "bool" {
		t.Errorf("map(fn): subst[R] = %v, want bool", got)
	}
}

// TestSelectOverloadSubstIsolation checks that each candidate solves its own
// substitution: a generic candidate's variable bindings must not leak into a
// sibling's selection (the per-candidate maps.Clone), in either direction.
func TestSelectOverloadSubstIsolation(t *testing.T) {
	reg := builtin.Default()
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	// pick(f: fn(T): R): R  /  pick(other: self): self — the same name with a
	// generic arm and a self arm.
	def := &ir.TypeDef{
		Name: "Chooser",
		Body: bt("int"),
		Methods: []*ir.Method{
			{Name: "pick", Params: []ir.Param{{Name: "f", Type: &ir.Func{Params: []ir.Type{tvar("T")}, Result: tvar("R")}}}, Result: tvar("R")},
			{Name: "pick", Params: []ir.Param{{Name: "other", Type: &ir.SelfType{}}}, Result: &ir.SelfType{}},
		},
	}
	chooser := &ir.Named{Def: def}

	// The function argument fits only the generic arm, solving T and R in its
	// own substitution.
	fn := &ir.Func{Params: []ir.Type{bt("nint")}, Result: bt("bool")}
	matches, _ := SelectOverload(reg, chooser, "pick", []ir.Type{fn})
	if len(matches) != 1 || len(matches[0].Method.Params) != 1 {
		t.Fatalf("pick(fn): matches = %d, want the generic arm alone", len(matches))
	}
	if got := matches[0].Subst["R"]; got == nil || got.String() != "bool" {
		t.Errorf("pick(fn): subst[R] = %v, want bool", got)
	}

	// The self argument fits only the self arm — and its substitution must
	// not have inherited the generic arm's failed trial bindings.
	matches, _ = SelectOverload(reg, chooser, "pick", []ir.Type{chooser})
	if len(matches) != 1 || matches[0].Method.Params[0].Type.String() != "self" {
		t.Fatalf("pick(self): matches = %d, want the self arm alone", len(matches))
	}
	if _, leaked := matches[0].Subst["T"]; leaked {
		t.Errorf("pick(self): the generic arm's T leaked into the self arm's substitution")
	}
	if _, leaked := matches[0].Subst["R"]; leaked {
		t.Errorf("pick(self): the generic arm's R leaked into the self arm's substitution")
	}
}

// TestOverloadedMethodResult checks MethodResult over an overload set: the
// unique fit's result, and Invalid for no fit or an ambiguous one.
func TestOverloadedMethodResult(t *testing.T) {
	reg := builtin.Default()
	score := overloadedScore()
	cases := []struct {
		name string
		args []ir.Type
		want string
	}{
		{"nint picks points", []ir.Type{bt("nint")}, "Score"},
		{"Score picks points", []ir.Type{score}, "Score"},
		{"bool picks flag", []ir.Type{bt("bool")}, "bool"},
		{"string fits neither", []ir.Type{bt("string")}, "invalid"},
		{"arity fits neither", nil, "invalid"},
	}
	for _, tc := range cases {
		if got := MethodResult(reg, score, "merge", tc.args).String(); got != tc.want {
			t.Errorf("%s: MethodResult(Score, merge, ...) = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestOverloadShadowing checks the level rule: a definition that declares a
// name itself shadows every same-name method it would derive from its
// underlying type, and ReceiverMethods keeps one level's overloads together.
func TestOverloadShadowing(t *testing.T) {
	reg := builtin.Default()
	// Tally redeclares add(bool) over int8: the derived add(self) is shadowed.
	tally := &ir.Named{Def: &ir.TypeDef{
		Name: "Tally",
		Body: bt("sbyte"),
		Methods: []*ir.Method{
			{Name: "add", Params: []ir.Param{{Name: "flag", Type: bt("bool")}}, Result: &ir.SelfType{}},
		},
	}}

	ms, _, ok := Candidates(reg, tally, "add")
	if !ok || len(ms) != 1 || ms[0].Params[0].Type.String() != "bool" {
		t.Fatalf("Candidates(Tally, add) = %d methods, want the own add(bool) alone", len(ms))
	}
	// Tally + Tally no longer resolves: the own add shadows the derived one.
	if got := MethodResult(reg, tally, "add", []ir.Type{tally}).String(); got != "invalid" {
		t.Errorf("MethodResult(Tally, add, Tally) = %s, want invalid (shadowed)", got)
	}
	// A method Tally does not declare still derives from int8.
	if got := MethodResult(reg, tally, "sub", []ir.Type{tally}).String(); got != "Tally" {
		t.Errorf("MethodResult(Tally, sub, Tally) = %s, want Tally", got)
	}

	// ReceiverMethods lists the own add(bool) once and never the derived add,
	// while an overloaded own name appears with every signature.
	score := overloadedScore()
	methods, _, ok := ReceiverMethods(reg, score)
	if !ok {
		t.Fatal("ReceiverMethods(Score) not found")
	}
	merges, adds := 0, 0
	for _, m := range methods {
		switch m.Name {
		case "merge":
			merges++
		case "add":
			adds++
		}
	}
	if merges != 2 {
		t.Errorf("ReceiverMethods(Score): %d merge signatures, want 2", merges)
	}
	if adds != 1 {
		t.Errorf("ReceiverMethods(Score): %d derived add signatures, want 1", adds)
	}
}

// TestNominalDerivation checks that a nominal type (type Level = int8) is
// integer-like, derives its underlying type's operator methods, and keeps its
// own identity in the result.
func TestNominalDerivation(t *testing.T) {
	reg := builtin.Default()
	level := &ir.Named{Def: &ir.TypeDef{
		Name: "Level",
		Body: bt("sbyte"),
		Methods: []*ir.Method{
			{Name: "increment", Result: &ir.SelfType{}},
		},
	}}

	if !IsInteger(reg, level) {
		t.Errorf("IsInteger(Level) = false, want true (underlying sbyte)")
	}

	// add is not declared on Level; it is derived from int8 and returns self,
	// which is Level. The default integer literal adapts to Level.
	if got := MethodResult(reg, level, "add", []ir.Type{bt("nint")}).String(); got != "Level" {
		t.Errorf("MethodResult(Level, add, nint) = %s, want Level", got)
	}
	// Level + Level is Level.
	if got := MethodResult(reg, level, "add", []ir.Type{level}).String(); got != "Level" {
		t.Errorf("MethodResult(Level, add, Level) = %s, want Level", got)
	}
	// A comparison derived from int8 returns bool.
	if got := MethodResult(reg, level, "lt", []ir.Type{bt("nint")}).String(); got != "bool" {
		t.Errorf("MethodResult(Level, lt, nint) = %s, want bool", got)
	}
	// Level's own method is found directly.
	if got := MethodResult(reg, level, "increment", nil).String(); got != "Level" {
		t.Errorf("MethodResult(Level, increment) = %s, want Level", got)
	}
	// Level does not implicitly convert to its underlying int8.
	if Assignable(reg, level, bt("sbyte")) {
		t.Errorf("Level should not be assignable to sbyte")
	}
	// The default integer adapts to Level.
	if !Assignable(reg, bt("nint"), level) {
		t.Errorf("nint should be assignable to the nominal integer Level")
	}
}
