package infer

// Tests for bidirectional call typing (callType in call.go). Shared fixtures
// (ast/type builders, stubEnv, report) live in infer_test.go.

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- bidirectional calls (callType) ------------------------------------------

// genericListEnv installs a list definition carrying generic methods — map
// (the R-solving headline) and fold (whose first argument solves A before the
// literal is checked, exercising the two-pass order).
func genericListEnv() stubEnv {
	env := emptyEnv()
	tvar := func(name string) ir.Type { return &ir.TypeVar{Name: name} }
	listDef := &ir.TypeDef{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}}
	listDef.Methods = []*ir.Method{
		{
			Name:   "map",
			Params: []ir.Param{{Name: "func", Type: &ir.Func{Params: []ir.Type{tvar("T")}, Result: tvar("R")}}},
			Result: &ir.App{Def: listDef, Args: []ir.Type{tvar("R")}},
		},
		{
			Name: "fold",
			Params: []ir.Param{
				{Name: "init", Type: tvar("A")},
				{Name: "func", Type: &ir.Func{Params: []ir.Type{tvar("A"), tvar("T")}, Result: tvar("A")}},
			},
			Result: tvar("A"),
		},
	}
	env.reg.Install([]*ir.TypeDef{listDef})
	return env
}

// mapCall builds [1, 2].map(arg).
func mapCall(method string, args ...ast.Expr) *ast.CallExpr {
	recv := listLit(intLit("1"), intLit("2"))
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(method, nil), nil)
	return ast.NewCallExpr(m, args, nil)
}

func TestCallTypeSolvesLambda(t *testing.T) {
	env := genericListEnv()

	// The headline: x is pushed in from T = int, R solves from the body.
	var r report
	got := Check(mapCall("map", funcLit([]*ast.ParamDef{param("x", nil)}, nil,
		ret(binary(ident("x"), "mul", intLit("2"))))), env, r.sink())
	if got.String() != "list<nint>" {
		t.Errorf("map(fn(x) { return x * 2 }) = %s, want list<nint>", got)
	}
	if len(r.methods)+len(r.mismatches)+len(r.uninferableParams)+r.uninferables != 0 {
		t.Errorf("unexpected reports: %+v", r)
	}

	// R = bool from a comparison body; the silent walk agrees (purity).
	lit := funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(binary(ident("x"), "lt", intLit("0"))))
	if got := Expr(mapCall("map", lit), env).String(); got != "list<bool>" {
		t.Errorf("Expr(map(fn(x) { return x < 0 })) = %s, want list<bool>", got)
	}
	if got := Check(mapCall("map", lit), env, nil).String(); got != "list<bool>" {
		t.Errorf("Check(map(fn(x) { return x < 0 })) = %s, want list<bool>", got)
	}
}

func TestCallTypeTwoPasses(t *testing.T) {
	// fold(init, fn(acc, x) { ... }): pass 1 solves A = int from init, pass 2
	// pushes fn(int, int) into the literal.
	env := genericListEnv()
	var r report
	got := Check(mapCall("fold", intLit("0"),
		funcLit([]*ast.ParamDef{param("acc", nil), param("x", nil)}, nil,
			ret(binary(ident("acc"), "add", ident("x"))))), env, r.sink())
	if got.String() != "nint" {
		t.Errorf("fold = %s, want nint", got)
	}
	if len(r.methods)+len(r.mismatches)+len(r.uninferableParams)+r.uninferables != 0 {
		t.Errorf("unexpected reports: %+v", r)
	}
}

// genericFnEnv installs an interface foldable<K, V> (required fold) and a Bag
// that opts into foldable<int, int>, plus three generic functions:
//
//	fn total<T: foldable<int, int>>(c: T): int
//	fn identity<T>(x: T): T
//	fn pick<T>(): T            // T appears only in the result — uninferable
func genericFnEnv() stubEnv {
	env := emptyEnv()
	foldable := &ir.TypeDef{
		Name:      "foldable",
		Interface: &ir.InterfaceDef{Required: []string{"fold"}},
		Params:    []*ir.TypeParam{{Name: "K"}, {Name: "V"}},
		Methods:   []*ir.Method{{Name: "fold", Result: &ir.TypeVar{Name: "V"}}},
	}
	bound := &ir.App{Def: foldable, Args: []ir.Type{&ir.Builtin{Name: "nint"}, &ir.Builtin{Name: "nint"}}}
	bag := &ir.TypeDef{Name: "Bag", Body: &ir.Builtin{Name: "nint"}, Impls: []ir.Type{bound}}
	env.reg.Install([]*ir.TypeDef{foldable, bag})

	// foldable<int, int> as a type expression for the bound.
	foldableBound := ast.NewNamedType("", "foldable", []ast.TypeExpr{namedType("nint"), namedType("nint")}, nil)
	total := ast.NewFuncDecl(nil, true, false, nil, "total",
		[]*ast.TypeParam{ast.NewTypeParam("T", foldableBound, nil)},
		[]*ast.ParamDef{param("c", namedType("T"))}, namedType("nint"),
		[]ast.Stmt{ret(intLit("0"))}, nil)
	identity := ast.NewFuncDecl(nil, true, false, nil, "identity",
		[]*ast.TypeParam{ast.NewTypeParam("T", nil, nil)},
		[]*ast.ParamDef{param("x", namedType("T"))}, namedType("T"),
		[]ast.Stmt{ret(ident("x"))}, nil)
	pick := ast.NewFuncDecl(nil, true, false, nil, "pick",
		[]*ast.TypeParam{ast.NewTypeParam("T", nil, nil)},
		nil, namedType("T"),
		[]ast.Stmt{ret(intLit("0"))}, nil)
	env.fns = map[string][]*ast.FuncDecl{
		"total":    {total},
		"identity": {identity},
		"pick":     {pick},
	}
	return env
}

// fnCall builds name(args...).
func fnCall(name string, args ...ast.Expr) *ast.CallExpr {
	return ast.NewCallExpr(ident(name), args, nil)
}

// TestFuncCallSolvesTypeParam checks a generic-function call resolves its type
// parameter from the argument and substitutes it into the result.
func TestFuncCallSolvesTypeParam(t *testing.T) {
	env := genericFnEnv()

	// identity(42): T = int, result T -> int.
	var r report
	if got := Check(fnCall("identity", intLit("42")), env, r.sink()).String(); got != "nint" {
		t.Errorf("identity(42) = %s, want nint", got)
	}
	if len(r.boundsNotSatisfied)+len(r.uninferableTypeVar)+len(r.methods) != 0 {
		t.Errorf("unexpected reports: %+v", r)
	}

	// The silent walk agrees (purity).
	if got := Expr(fnCall("identity", intLit("42")), env).String(); got != "nint" {
		t.Errorf("Expr(identity(42)) = %s, want nint", got)
	}
}

// TestFuncCallBoundSatisfied checks a bound the argument satisfies types the
// call, while one it does not is reported as bound_not_satisfied.
func TestFuncCallBoundSatisfied(t *testing.T) {
	env := genericFnEnv()
	bagType := func() ir.Type {
		d, _ := env.reg.Lookup("Bag")
		return &ir.Named{Def: d}
	}()

	// A Bag value satisfies foldable<int, int>. The argument is a const ref
	// typed Bag.
	decl := ast.NewConstDecl(nil, false, "b", nil, nil, nil)
	id := ident("b")
	env.res[id] = decl
	env.typ[decl] = bagType
	var r report
	if got := Check(fnCall("total", id), env, r.sink()).String(); got != "nint" {
		t.Errorf("total(Bag) = %s, want nint", got)
	}
	if len(r.boundsNotSatisfied) != 0 {
		t.Errorf("Bag satisfies foldable<nint, nint>; unexpected bound reports: %+v", r.boundsNotSatisfied)
	}

	// A plain int does not opt into foldable: bound_not_satisfied.
	var r2 report
	if got := Check(fnCall("total", intLit("1")), env, r2.sink()); got != ir.Invalid {
		t.Errorf("total(1) = %s, want invalid", got)
	}
	if len(r2.boundsNotSatisfied) != 1 || r2.boundsNotSatisfied[0] != "nint -> foldable<nint, nint>" {
		t.Errorf("want [nint -> foldable<nint, nint>], got %+v", r2.boundsNotSatisfied)
	}
}

// TestFuncCallUninferableTypeParam checks a type parameter no argument pins
// (the T appears only in the result) is reported as uninferable.
func TestFuncCallUninferableTypeParam(t *testing.T) {
	env := genericFnEnv()
	var r report
	if got := Check(fnCall("pick"), env, r.sink()); got != ir.Invalid {
		t.Errorf("pick() = %s, want invalid", got)
	}
	if len(r.uninferableTypeVar) != 1 || r.uninferableTypeVar[0] != "T" {
		t.Errorf("want one uninferable type param T, got %+v", r.uninferableTypeVar)
	}
}

// TestFuncCallOverloadCrossArgConsistency checks the overloaded generic path
// enforces cross-argument type-variable consistency: pair is overloaded with a
// non-generic arity-1 form and a generic pair<T>(a: T, b: T) form, and
// pair(7, "x") binds T to int then string — which must be reported, not
// silently accepted. Before the fix, each argument matched against a fresh
// substitution, so the inconsistency was invisible and the call typed as int.
func TestFuncCallOverloadCrossArgConsistency(t *testing.T) {
	env := emptyEnv()
	wrapStr := ast.NewFuncDecl(nil, true, false, nil, "pair", nil,
		[]*ast.ParamDef{param("s", namedType("string"))}, namedType("string"),
		[]ast.Stmt{ret(ident("s"))}, nil)
	wrapGen := ast.NewFuncDecl(nil, true, false, nil, "pair",
		[]*ast.TypeParam{ast.NewTypeParam("T", nil, nil)},
		[]*ast.ParamDef{param("a", namedType("T")), param("b", namedType("T"))}, namedType("T"),
		[]ast.Stmt{ret(ident("a"))}, nil)
	env.fns = map[string][]*ast.FuncDecl{"pair": {wrapStr, wrapGen}}

	var r report
	got := Check(fnCall("pair", intLit("7"), stringLit("x")), env, r.sink())
	if got != ir.Invalid {
		t.Errorf("pair(7, \"x\") = %s, want invalid (T cannot be both nint and string)", got)
	}
	if len(r.mismatches) != 1 || r.mismatches[0] != "string -> nint" {
		t.Errorf("want one mismatch [string -> nint], got %+v", r.mismatches)
	}

	// The consistent call still types as the substituted result.
	var r2 report
	if got := Check(fnCall("pair", intLit("7"), intLit("9")), env, r2.sink()).String(); got != "nint" {
		t.Errorf("pair(7, 9) = %s, want nint", got)
	}
	if len(r2.mismatches)+len(r2.noMatchingFunc) != 0 {
		t.Errorf("a consistent call must not report: %+v", r2)
	}
}

func TestCallTypeLambdaFailures(t *testing.T) {
	env := genericListEnv()

	// No return: R stays unsolved — the literal's own diagnostic fires, the
	// call is Invalid, and no invalid_operation piles on.
	var r report
	got := Check(mapCall("map", funcLit([]*ast.ParamDef{param("x", nil)}, nil)), env, r.sink())
	if got != ir.Invalid {
		t.Errorf("map(fn(x) {}) = %s, want invalid", got)
	}
	if r.uninferables != 1 || len(r.methods) != 0 {
		t.Errorf("want one uninferable result and no invalid_operation, got %+v", r)
	}

	// Wrong parameter count: same suppression.
	var r2 report
	got = Check(mapCall("map", funcLit([]*ast.ParamDef{param("x", nil), param("y", nil)}, nil, ret(ident("x")))), env, r2.sink())
	if got != ir.Invalid {
		t.Errorf("map(fn(x, y)) = %s, want invalid", got)
	}
	if len(r2.arities) != 1 || len(r2.methods) != 0 {
		t.Errorf("want one arity mismatch and no invalid_operation, got %+v", r2)
	}

	// A conflicting annotation: reported at the parameter, call Invalid.
	var r3 report
	got = Check(mapCall("map", funcLit([]*ast.ParamDef{param("x", namedType("string"))}, nil, ret(ident("x")))), env, r3.sink())
	if got != ir.Invalid {
		t.Errorf("map(fn(x: string)) = %s, want invalid", got)
	}
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "string -> nint" || len(r3.methods) != 0 {
		t.Errorf("want [string -> nint] and no invalid_operation, got %+v", r3)
	}

	// A non-lambda argument that does not fit still reports the call itself.
	var r4 report
	got = Check(mapCall("map", intLit("1")), env, r4.sink())
	if got != ir.Invalid {
		t.Errorf("map(1) = %s, want invalid", got)
	}
	if len(r4.methods) != 1 || r4.methods[0] != "map" {
		t.Errorf("want the invalid_operation on map, got %+v", r4)
	}
}
