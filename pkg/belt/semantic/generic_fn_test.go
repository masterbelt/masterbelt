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
	"pub type Bag = list<nint> impl foldable<nint, nint> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: nint, value: nint): A): A {\n" +
	"    return init\n" +
	"  }\n" +
	"}\n"

// TestGenericFuncResolvesTypeParams checks a generic function resolves its type
// parameters into the IR: a bound carries its interface application, a parameter
// or result naming a type parameter is a TypeVar.
func TestGenericFuncResolvesTypeParams(t *testing.T) {
	src := "pub fn id<T>(x: T): T { return x }\n" +
		genericFoldableSrc +
		"pub fn total<T: foldable<nint, nint>>(c: T): nint {\n" +
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
	if total.TypeParams[0].Bound.String() != "foldable<nint, nint>" {
		t.Errorf("total bound = %s, want foldable<nint, nint>", total.TypeParams[0].Bound)
	}
	if total.Params[0].Type.String() != "T" || total.Result.String() != "nint" {
		t.Errorf("total signature = (%s): %s, want (T): nint", total.Params[0].Type, total.Result)
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
	if n == nil || n.Type.String() != "nint" {
		t.Fatalf("N type = %v, want nint", n)
	}
	if n.Eval == nil || n.Eval.String() != "42" {
		t.Errorf("N eval = %v, want 42 (the call folds with T = nint)", n.Eval)
	}
	p := constOf(m, "P")
	if p == nil || p.Type.String() != "nint" || p.Eval == nil || p.Eval.String() != "7" {
		t.Errorf("P = %+v, want nint folding to 7", p)
	}
}

// TestGenericFuncBoundNotSatisfied checks calling a bounded function with a type
// that does not implement the bound is reported.
func TestGenericFuncBoundNotSatisfied(t *testing.T) {
	src := genericFoldableSrc +
		"pub fn total<T: foldable<nint, nint>>(c: T): nint {\n" +
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
		"pub fn total<T: foldable<nint, nint>>(c: T): nint {\n" +
		"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
		"}\n" +
		"const B: Bag = [1, 2, 3]\n" +
		"const Sum = total(B)\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("Bag implements foldable<nint, nint>; unexpected bound_not_satisfied: %v", codes(diags))
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
	src := "pub fn f<T>(x: T): nint { return x.foo() }\n"
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
		"pub fn total<T: foldable<nint, nint>>(c: T): nint {\n" +
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
	src := "pub fn f<T: bogus>(x: T): nint { return 0 }\n"
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
	if r == nil || r.Type.String() != "nint" {
		t.Fatalf("R type = %v, want nint (the result type variable must be substituted)", r)
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
		"pub fn use1(s: string): nint { return 0 }\n" +
		"pub fn use1<T: foldable<nint, nint>>(c: T, d: T): nint { return 0 }\n" +
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
		"pub fn make<T>(a: nint, b: nint): T { return make(\"x\") }\n" +
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
		"pub fn pair(s: string): nint { return 0 }\n" +
		"pub fn pair<T: foldable<nint, nint>>(a: T, b: T): nint { return 0 }\n" +
		"const B = Bag([1, 2, 3])\n"

	good := prelude + "const X = pair(B, 1)\n"
	if _, diags := analyze(good); !hasCode(diags, CodeTypeMismatch) && !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("pair(B, 1): want a diagnostic (Bag/nint disagree), got %v", codes(diags))
	}

	rev := prelude + "const Y = pair(1, B)\n"
	if _, diags := analyze(rev); !hasCode(diags, CodeTypeMismatch) && !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("pair(1, B): want a diagnostic (nint/Bag disagree), got %v", codes(diags))
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
		t.Fatalf("unwrap(1) should solve T = nint; got %v", codes(diags))
	}
	r := constOf(m, "R")
	if r == nil || r.Type.String() != "nint" {
		t.Fatalf("R type = %v, want nint (T solved through the union)", r)
	}
}

// TestGenericFuncSolvesThroughRecordParam checks a type parameter nested inside
// a record parameter ({ v: T }) is solved from the argument's same-named field.
func TestGenericFuncSolvesThroughRecordParam(t *testing.T) {
	src := "pub type Box = { v: nint }\n" +
		"pub fn first<T>(p: { v: T }): T { return p.v }\n" +
		"const R = first(Box{ v: 1 })\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("first(Box{ v: 1 }) should solve T = nint; got %v", codes(diags))
	}
	r := constOf(m, "R")
	if r == nil || r.Type.String() != "nint" {
		t.Fatalf("R type = %v, want nint (T solved through the record field)", r)
	}
}

// TestGenericFuncOperatorBoundOverBuiltins checks the operator contracts the
// prelude declares — comparable, orderable, numeric — work as generic bounds over
// the builtin types that opt into them. A bounded parameter calls the bound
// interface's operator method in the body (the bounded-TypeVar method resolution),
// and a call with a conforming builtin argument is accepted while a non-conforming
// one is reported. These pin the operator-interface payoff: write the bound, not
// the type.
func TestGenericFuncOperatorBoundOverBuiltins(t *testing.T) {
	// orderable: max calls a.gteq(b) on the bounded parameter; int and string
	// both impl orderable, so both calls type-check and bind T.
	t.Run("orderable", func(t *testing.T) {
		src := "pub fn max<T: orderable>(a: T, b: T): T {\n" +
			"  return a.gteq(b) ? a : b\n" +
			"}\n" +
			"const I = max(3, 7)\n" +
			"const S = max(\"a\", \"b\")\n"
		m, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("max over orderable builtins should be clean; got %v", codes(diags))
		}
		if i := constOf(m, "I"); i == nil || i.Type.String() != "nint" {
			t.Errorf("I type = %v, want nint", i)
		}
		if s := constOf(m, "S"); s == nil || s.Type.String() != "string" {
			t.Errorf("S type = %v, want string", s)
		}
	})

	// comparable: same calls a.eql(b) on the bounded parameter; bool and int
	// both impl comparable.
	t.Run("comparable", func(t *testing.T) {
		src := "pub fn same<T: comparable>(a: T, b: T): bool {\n" +
			"  return a.eql(b)\n" +
			"}\n" +
			"const A = same(1, 1)\n" +
			"const B = same(true, false)\n"
		m, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("same over comparable builtins should be clean; got %v", codes(diags))
		}
		if a := constOf(m, "A"); a == nil || a.Type.String() != "bool" {
			t.Errorf("A type = %v, want bool", a)
		}
		if b := constOf(m, "B"); b == nil || b.Type.String() != "bool" {
			t.Errorf("B type = %v, want bool", b)
		}
	})

	// numeric: twice calls a.add(a) on the bounded parameter; int impls numeric.
	t.Run("numeric", func(t *testing.T) {
		src := "pub fn twice<T: numeric>(a: T): T {\n" +
			"  return a.add(a)\n" +
			"}\n" +
			"const N = twice(21)\n"
		m, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("twice over a numeric builtin should be clean; got %v", codes(diags))
		}
		if n := constOf(m, "N"); n == nil || n.Type.String() != "nint" {
			t.Errorf("N type = %v, want nint", n)
		}
	})

	// A type that does not opt into the bound is reported. string impls
	// comparable and orderable but not numeric, so twice("x") fails the bound.
	t.Run("not satisfied", func(t *testing.T) {
		src := "pub fn twice<T: numeric>(a: T): T {\n" +
			"  return a.add(a)\n" +
			"}\n" +
			"const Bad = twice(\"x\")\n"
		_, diags := analyze(src)
		if !hasCode(diags, CodeBoundNotSatisfied) {
			t.Fatalf("string does not impl numeric; want bound_not_satisfied, got %v", codes(diags))
		}
	})
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
