package infer

// Tests for scope resolution and chaining (funcScope/constScope/BodyScope in
// scope.go): a body leaf delegating to the enclosing scope, nested literal
// scopes, and a parameter shadowing an outer const. Shared fixtures live in
// infer_test.go.

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestFuncLitBodySeesOuterScope(t *testing.T) {
	// A body leaf that is not a parameter delegates to the enclosing scope: a
	// reference to a constant types as that constant.
	env := emptyEnv()
	decl := ast.NewConstDecl(nil, false, "A", nil, intLit("1"), nil)
	id := ident("A")
	env.res[id] = decl
	env.typ[decl] = &ir.Builtin{Name: "int"}

	lit := funcLit(nil, nil, ret(id))
	if got := Expr(lit, env).String(); got != "fn(): int" {
		t.Errorf("Expr = %s, want fn(): int", got)
	}
}

func TestFuncLitNestedScopes(t *testing.T) {
	// A nested literal's body sees its own parameter first and the outer
	// literal's parameters through the chained scope.
	env := emptyEnv()
	inner := funcLit(
		[]*ast.ParamDef{param("y", namedType("sbyte"))},
		nil,
		ret(binary(ident("y"), "add", ident("x"))), // y: int8, x: the outer int
	)
	outer := funcLit([]*ast.ParamDef{param("x", namedType("nint"))}, nil, ret(inner))
	if got := Expr(outer, env).String(); got != "fn(nint): fn(sbyte): sbyte" {
		t.Errorf("Expr = %s, want fn(nint): fn(sbyte): sbyte", got)
	}
}

func TestFuncLitParamShadowsOuter(t *testing.T) {
	// A parameter named like a constant shadows it inside the body.
	env := emptyEnv()
	decl := ast.NewConstDecl(nil, false, "x", nil, boolLit(true), nil)
	id := ident("x")
	env.res[id] = decl
	env.typ[decl] = &ir.Builtin{Name: "bool"}

	lit := funcLit([]*ast.ParamDef{param("x", namedType("nint"))}, nil, ret(id))
	if got := Expr(lit, env).String(); got != "fn(nint): nint" {
		t.Errorf("Expr = %s, want fn(nint): nint (the parameter shadows the const)", got)
	}
}

// TestBoundedTypeVarMethods checks that in a function body a parameter typed as
// a bounded type variable resolves its bound interface's methods, while an
// unbounded one has none and a method call on it is reported as the distinct
// no_method_on_unbounded_typevar (E-17).
func TestBoundedTypeVarMethods(t *testing.T) {
	reg := builtin.Default()
	foldable := &ir.TypeDef{
		Name:      "foldable",
		Interface: &ir.InterfaceDef{Required: []string{"fold"}},
		Params:    []*ir.TypeParam{{Name: "K"}, {Name: "V"}},
		Methods:   []*ir.Method{{Name: "fold", Result: &ir.TypeVar{Name: "V"}}},
	}
	reg.Install([]*ir.TypeDef{foldable})
	bound := &ir.App{Def: foldable, Args: []ir.Type{&ir.Builtin{Name: "nint"}, &ir.Builtin{Name: "nint"}}}

	universe := map[string]*ir.TypeDef{"foldable": foldable}

	// A bounded parameter: c.fold() resolves through the bound, V = int.
	bs := BodyScope{Reg: reg, Universe: universe, Self: ir.Invalid,
		Params: map[string]ir.Type{"c": &ir.TypeVar{Name: "T", Bound: bound}}}
	foldCall := ast.NewCallExpr(ast.NewMemberExpr(ident("c"), ast.NewIdentifier("fold", nil), nil), nil, nil)
	var r report
	if got := CheckBody(foldCall, ir.Invalid, bs, r.sink()).String(); got != "nint" {
		t.Errorf("c.fold() on c: T (T: foldable<nint, nint>) = %s, want nint", got)
	}
	if len(r.unboundedMethods) != 0 || len(r.methods) != 0 {
		t.Errorf("a bounded parameter has its interface methods; unexpected reports: %+v", r)
	}

	// An unbounded parameter: x.foo() is the distinct error, not invalid_op.
	bs2 := BodyScope{Reg: reg, Universe: universe, Self: ir.Invalid,
		Params: map[string]ir.Type{"x": &ir.TypeVar{Name: "T"}}}
	fooCall := ast.NewCallExpr(ast.NewMemberExpr(ident("x"), ast.NewIdentifier("foo", nil), nil), nil, nil)
	var r2 report
	if got := CheckBody(fooCall, ir.Invalid, bs2, r2.sink()); got != ir.Invalid {
		t.Errorf("x.foo() on unbounded x: T = %s, want invalid", got)
	}
	if len(r2.unboundedMethods) != 1 || r2.unboundedMethods[0] != "foo" {
		t.Errorf("want one no_method_on_unbounded_typevar for foo, got %+v", r2.unboundedMethods)
	}
	if len(r2.methods) != 0 {
		t.Errorf("an unbounded-typevar method call must not also fire invalid_operation: %+v", r2.methods)
	}
}
