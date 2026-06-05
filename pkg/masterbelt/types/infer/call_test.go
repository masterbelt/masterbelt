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
	if got.String() != "list<int>" {
		t.Errorf("map(fn(x) { return x * 2 }) = %s, want list<int>", got)
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
	if got.String() != "int" {
		t.Errorf("fold = %s, want int", got)
	}
	if len(r.methods)+len(r.mismatches)+len(r.uninferableParams)+r.uninferables != 0 {
		t.Errorf("unexpected reports: %+v", r)
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
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "string -> int" || len(r3.methods) != 0 {
		t.Errorf("want [string -> int] and no invalid_operation, got %+v", r3)
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
