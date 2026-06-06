package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// genericFoldableSrc is the interface and an opt-in implementor a generic
// function's bound refers to: foldable<K, V> with a required fold, and a Bag of
// ints that implements foldable<int, int>.
const genericFoldableSrc = "" +
	"pub interface foldable<K, V> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: K, value: V): A): A\n" +
	"}\n" +
	"pub type Bag = list<int> impl foldable<int, int> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: int, value: int): A): A {\n" +
	"    return init\n" +
	"  }\n" +
	"}\n"

// TestGenericFuncResolvesTypeParams checks a generic function resolves its type
// parameters into the IR: a bound carries its interface application, a parameter
// or result naming a type parameter is a TypeVar.
func TestGenericFuncResolvesTypeParams(t *testing.T) {
	src := "pub fn id<T>(x: T): T { return x }\n" +
		genericFoldableSrc +
		"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	id := funcOf(m, "id")
	if id == nil || len(id.TypeParams) != 1 || id.TypeParams[0].Name != "T" || id.TypeParams[0].Bound != nil {
		t.Fatalf("id type params = %+v, want one unbounded T", id)
	}
	if id.Params[0].Type.String() != "T" || id.Result.String() != "T" {
		t.Errorf("id signature = (%s): %s, want (T): T", id.Params[0].Type, id.Result)
	}

	total := funcOf(m, "total")
	if total == nil || len(total.TypeParams) != 1 || total.TypeParams[0].Bound == nil {
		t.Fatalf("total type params = %+v, want one bounded T", total)
	}
	if total.TypeParams[0].Bound.String() != "foldable<int, int>" {
		t.Errorf("total bound = %s, want foldable<int, int>", total.TypeParams[0].Bound)
	}
	if total.Params[0].Type.String() != "T" || total.Result.String() != "int" {
		t.Errorf("total signature = (%s): %s, want (T): int", total.Params[0].Type, total.Result)
	}
}

// TestGenericFuncCallSolvesAndFolds checks a call resolves the type parameter
// from the argument, types as the substituted result, and folds at compile time.
func TestGenericFuncCallSolvesAndFolds(t *testing.T) {
	src := "pub fn id<T>(x: T): T { return x }\n" +
		"pub fn firstOf<T>(a: T, b: T): T { return a }\n" +
		"const N = id(42)\n" +
		"const P = firstOf(7, 9)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	n := constOf(m, "N")
	if n == nil || n.Type.String() != "int" {
		t.Fatalf("N type = %v, want int", n)
	}
	if n.Eval == nil || n.Eval.String() != "42" {
		t.Errorf("N eval = %v, want 42 (the call folds with T = int)", n.Eval)
	}
	p := constOf(m, "P")
	if p == nil || p.Type.String() != "int" || p.Eval == nil || p.Eval.String() != "7" {
		t.Errorf("P = %+v, want int folding to 7", p)
	}
}

// TestGenericFuncBoundNotSatisfied checks calling a bounded function with a type
// that does not implement the bound is reported.
func TestGenericFuncBoundNotSatisfied(t *testing.T) {
	src := genericFoldableSrc +
		"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n" +
		"const Bad = total(42)\n" // int does not implement foldable<int, int>
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied, got %v", codes(diags))
	}
}

// TestGenericFuncBoundSatisfied checks calling a bounded function with a type
// that does implement the bound is accepted.
func TestGenericFuncBoundSatisfied(t *testing.T) {
	src := genericFoldableSrc +
		"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n" +
		"const B: Bag = [1, 2, 3]\n" +
		"const Sum = total(B)\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("Bag implements foldable<int, int>; unexpected bound_not_satisfied: %v", codes(diags))
	}
}

// TestGenericFuncUninferableTypeParam checks a type parameter that no argument
// pins (it appears only in the result) is reported.
func TestGenericFuncUninferableTypeParam(t *testing.T) {
	src := "pub fn pick<T>(): T { return pick() }\n" +
		"const X = pick()\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUninferableTypeParam) {
		t.Fatalf("want uninferable_type_param, got %v", codes(diags))
	}
}

// TestGenericFuncUnboundedMethodBan checks calling a method on an unbounded type
// parameter in the body is the distinct error, not invalid_operation.
func TestGenericFuncUnboundedMethodBan(t *testing.T) {
	src := "pub fn f<T>(x: T): int { return x.foo() }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeNoMethodOnUnboundedTypevar) {
		t.Fatalf("want no_method_on_unbounded_typevar, got %v", codes(diags))
	}
	if hasCode(diags, CodeInvalidOperation) {
		t.Errorf("an unbounded-typevar method call must not also fire invalid_operation: %v", codes(diags))
	}
}

// TestGenericFuncBoundedMethodAllowed checks a bounded parameter can call its
// bound interface's methods in the body without an error.
func TestGenericFuncBoundedMethodAllowed(t *testing.T) {
	src := genericFoldableSrc +
		"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a bounded parameter may call its interface methods; got %v", codes(diags))
	}
}

// TestGenericFuncUnknownBound checks an unknown interface in a bound is reported
// as an unknown type.
func TestGenericFuncUnknownBound(t *testing.T) {
	src := "pub fn f<T: bogus>(x: T): int { return 0 }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("want unknown_type for the undefined bound, got %v", codes(diags))
	}
}

// TestGenericFuncOverloadResultSubstitution checks that when an overload set
// includes a generic function and the call selects it, the solved type
// parameters are substituted into the result — the overloaded path must finish
// like the single-signature one, not leave the variable in the result type.
func TestGenericFuncOverloadResultSubstitution(t *testing.T) {
	// wrap is overloaded: a non-generic string form (arity 1) and a generic form
	// (arity 2). wrap(7, 9) selects the generic one, solves T = int, and the
	// result T must substitute to int (and the call fold to 7).
	src := "pub fn wrap(s: string): string { return s }\n" +
		"pub fn wrap<T>(a: T, b: T): T { return a }\n" +
		"const R = wrap(7, 9)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	r := constOf(m, "R")
	if r == nil || r.Type.String() != "int" {
		t.Fatalf("R type = %v, want int (the result type variable must be substituted)", r)
	}
	if r.Eval == nil || r.Eval.String() != "7" {
		t.Errorf("R eval = %v, want 7 (the selected generic overload folds)", r.Eval)
	}
}

// TestGenericFuncOverloadBoundNotSatisfied checks the bound check runs on the
// overloaded path too: selecting a bounded generic overload with an argument
// that does not implement the bound is reported.
func TestGenericFuncOverloadBoundNotSatisfied(t *testing.T) {
	// use1 is overloaded: a non-generic string form (arity 1) and a bounded
	// generic form (arity 2). use1(1, 2) selects the generic one, solves T = int,
	// and int does not implement foldable<int, int>.
	src := genericFoldableSrc +
		"pub fn use1(s: string): int { return 0 }\n" +
		"pub fn use1<T: foldable<int, int>>(c: T, d: T): int { return 0 }\n" +
		"const X = use1(1, 2)\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("want bound_not_satisfied on the overloaded path, got %v", codes(diags))
	}
}

// TestGenericFuncOverloadUninferable checks the uninferable check runs on the
// overloaded path too: a type parameter no argument pins (it appears only in the
// result) is reported when the overloaded generic candidate is selected.
func TestGenericFuncOverloadUninferable(t *testing.T) {
	// make is overloaded: a non-generic string form (arity 1) and a generic form
	// whose T appears only in the result (arity 2). make(1, 2) selects the
	// generic one; nothing pins T.
	src := "pub fn make(s: string): string { return s }\n" +
		"pub fn make<T>(a: int, b: int): T { return make(\"x\") }\n" +
		"const Y = make(1, 2)\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUninferableTypeParam) {
		t.Fatalf("want uninferable_type_param on the overloaded path, got %v", codes(diags))
	}
}

// TestGenericFuncOverloadCrossArgumentConsistency checks that when an overload
// set selects a generic candidate, a single type parameter bound to different
// concrete types by two arguments is reported — exactly as the single-signature
// path reports it. The overloaded path must not let arg 0 (T = int) and arg 1
// (T = string) disagree silently.
func TestGenericFuncOverloadCrossArgumentConsistency(t *testing.T) {
	// The single-signature baseline correctly reports the mismatch.
	base := "pub fn pair<T>(a: T, b: T): T { return a }\n" +
		"const R = pair(7, \"x\")\n"
	if _, diags := analyze(base); !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("single-signature baseline: want type_mismatch for pair(7, \"x\"), got %v", codes(diags))
	}

	// The overloaded form must report it too, not accept the ill-typed call.
	src := "pub fn wrap(s: string): string { return s }\n" +
		"pub fn wrap<T>(a: T, b: T): T { return a }\n" +
		"const R = wrap(7, \"x\")\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("overloaded path: want type_mismatch for wrap(7, \"x\"), got %v", codes(diags))
	}
}

// TestGenericFuncOverloadBoundCheckOrderIndependent checks the overloaded
// generic path does not depend on argument order: a bounded generic overload
// selected with a Bag (which satisfies the bound) and a bad int (which neither
// matches T = Bag nor satisfies the bound) is reported whichever way the two
// arguments are written. Before the fix, Bag first solved T = Bag and silently
// dropped the inconsistent int, while int first solved T = int and reported the
// bound — the result depended on order. B is a clean Bag value (a conversion,
// so the value itself carries no diagnostic).
func TestGenericFuncOverloadBoundCheckOrderIndependent(t *testing.T) {
	prelude := genericFoldableSrc +
		"pub fn pair(s: string): int { return 0 }\n" +
		"pub fn pair<T: foldable<int, int>>(a: T, b: T): int { return 0 }\n" +
		"const B = Bag([1, 2, 3])\n"

	good := prelude + "const X = pair(B, 1)\n"
	if _, diags := analyze(good); !hasCode(diags, CodeTypeMismatch) && !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("pair(B, 1): want a diagnostic (Bag/int disagree), got %v", codes(diags))
	}

	rev := prelude + "const Y = pair(1, B)\n"
	if _, diags := analyze(rev); !hasCode(diags, CodeTypeMismatch) && !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("pair(1, B): want a diagnostic (int/Bag disagree), got %v", codes(diags))
	}
}

// TestGenericFuncSolvesThroughUnionParam checks a type parameter nested inside a
// union parameter (T | error — the central unwrap use-case) is solved from the
// argument and substituted into the result.
func TestGenericFuncSolvesThroughUnionParam(t *testing.T) {
	src := "pub fn unwrap<T>(x: T | error): T { return x }\n" +
		"const R = unwrap(1)\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unwrap(1) should solve T = int; got %v", codes(diags))
	}
	r := constOf(m, "R")
	if r == nil || r.Type.String() != "int" {
		t.Fatalf("R type = %v, want int (T solved through the union)", r)
	}
}

// TestGenericFuncSolvesThroughRecordParam checks a type parameter nested inside
// a record parameter ({ v: T }) is solved from the argument's same-named field.
func TestGenericFuncSolvesThroughRecordParam(t *testing.T) {
	src := "pub type Box = { v: int }\n" +
		"pub fn first<T>(p: { v: T }): T { return p.v }\n" +
		"const R = first(Box{ v: 1 })\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("first(Box{ v: 1 }) should solve T = int; got %v", codes(diags))
	}
	r := constOf(m, "R")
	if r == nil || r.Type.String() != "int" {
		t.Fatalf("R type = %v, want int (T solved through the record field)", r)
	}
}

// funcOf returns the resolved function of the given name in the module, or nil.
func funcOf(m *ir.Module, name string) *ir.Function {
	for _, f := range m.Funcs {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// constOf returns the resolved constant of the given name in the module, or nil.
func constOf(m *ir.Module, name string) *ir.Const {
	for _, c := range m.Consts {
		if c.Name == name {
			return c
		}
	}
	return nil
}
