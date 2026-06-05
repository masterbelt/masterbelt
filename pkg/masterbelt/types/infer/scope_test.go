package infer

// Tests for scope resolution and chaining (funcScope/constScope/BodyScope in
// scope.go): a body leaf delegating to the enclosing scope, nested literal
// scopes, and a parameter shadowing an outer const. Shared fixtures live in
// infer_test.go.

import (
	"testing"

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
	env.typ[decl] = &ir.Builtin{Name: "int32"}

	lit := funcLit(nil, nil, ret(id))
	if got := Expr(lit, env).String(); got != "fn(): int32" {
		t.Errorf("Expr = %s, want fn(): int32", got)
	}
}

func TestFuncLitNestedScopes(t *testing.T) {
	// A nested literal's body sees its own parameter first and the outer
	// literal's parameters through the chained scope.
	env := emptyEnv()
	inner := funcLit(
		[]*ast.ParamDef{param("y", namedType("int8"))},
		nil,
		ret(binary(ident("y"), "add", ident("x"))), // y: int8, x: the outer int
	)
	outer := funcLit([]*ast.ParamDef{param("x", namedType("int"))}, nil, ret(inner))
	if got := Expr(outer, env).String(); got != "fn(int): fn(int8): int8" {
		t.Errorf("Expr = %s, want fn(int): fn(int8): int8", got)
	}
}

func TestFuncLitParamShadowsOuter(t *testing.T) {
	// A parameter named like a constant shadows it inside the body.
	env := emptyEnv()
	decl := ast.NewConstDecl(nil, false, "x", nil, boolLit(true), nil)
	id := ident("x")
	env.res[id] = decl
	env.typ[decl] = &ir.Builtin{Name: "bool"}

	lit := funcLit([]*ast.ParamDef{param("x", namedType("int"))}, nil, ret(id))
	if got := Expr(lit, env).String(); got != "fn(int): int" {
		t.Errorf("Expr = %s, want fn(int): int (the parameter shadows the const)", got)
	}
}
