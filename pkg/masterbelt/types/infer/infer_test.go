package infer

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// --- ast builders (nil syntax: the type rules never read Syntax) ------------

func intLit(text string) *ast.IntLit    { return ast.NewIntLit(text, nil) }
func boolLit(v bool) *ast.BoolLit       { return ast.NewBoolLit(v, nil) }
func ident(name string) *ast.Identifier { return ast.NewIdentifier(name, nil) }

// binary builds the desugared form of an operator: recv.method(arg).
func binary(recv ast.Expr, method string, arg ast.Expr) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(method, nil), nil)
	return ast.NewCallExpr(m, []ast.Expr{arg}, nil)
}

// unary builds the desugared form of a unary operator: recv.method().
func unary(recv ast.Expr, method string) *ast.CallExpr {
	m := ast.NewMemberExpr(recv, ast.NewIdentifier(method, nil), nil)
	return ast.NewCallExpr(m, nil, nil)
}

// stubEnv is a fixed resolution/typing environment for driving inference
// without the semantic engine. Type annotations resolve through the shared
// TypeResolver (Decl builds one from the registry), so the stub need only supply
// resolution and the registry.
type stubEnv struct {
	res map[*ast.Identifier]*ast.ConstDecl
	typ map[*ast.ConstDecl]ir.Type
	reg *builtin.Registry
}

func (e stubEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.res[id] }
func (e stubEnv) TypeOf(decl *ast.ConstDecl) ir.Type        { return e.typ[decl] }
func (e stubEnv) Registry() *builtin.Registry               { return e.reg }

func emptyEnv() stubEnv {
	return stubEnv{
		res: map[*ast.Identifier]*ast.ConstDecl{},
		typ: map[*ast.ConstDecl]ir.Type{},
		reg: builtin.Default(),
	}
}

func TestExprLiterals(t *testing.T) {
	env := emptyEnv()
	if got := Expr(intLit("1"), env).String(); got != "int" {
		t.Errorf("Expr(int literal) = %s, want int", got)
	}
	if got := Expr(boolLit(true), env).String(); got != "bool" {
		t.Errorf("Expr(bool literal) = %s, want bool", got)
	}
}

func TestExprReference(t *testing.T) {
	env := emptyEnv()
	decl := ast.NewConstDecl(nil, false, "A", nil, intLit("1"), nil)
	id := ident("A")
	int32Type := &ir.Builtin{Name: "int32"}
	env.res[id] = decl
	env.typ[decl] = int32Type

	// A reference inherits its referent's type.
	if got := Expr(id, env); got != int32Type {
		t.Errorf("Expr(ref to int32) = %s, want int32", got)
	}
	// An unresolved reference is Invalid.
	if got := Expr(ident("Missing"), env); got != ir.Invalid {
		t.Errorf("Expr(unresolved) = %s, want invalid", got)
	}
}

func TestExprCalls(t *testing.T) {
	env := emptyEnv()
	cases := []struct {
		name string
		expr ast.Expr
		want string
	}{
		{"add int literals", binary(intLit("1"), "add", intLit("2")), "int"},
		{"lt yields bool", binary(intLit("1"), "lt", intLit("2")), "bool"},
		{"and yields bool", binary(boolLit(true), "anan", boolLit(false)), "bool"},
		{"neg preserves type", unary(intLit("1"), "neg"), "int"},
		{"arith on bool is invalid", binary(boolLit(true), "add", intLit("1")), "invalid"},
		{"nested propagates invalid", binary(binary(boolLit(true), "add", intLit("1")), "add", intLit("2")), "invalid"},
		{"callee not a member", ast.NewCallExpr(intLit("1"), nil, nil), "invalid"},
	}
	for _, tc := range cases {
		if got := Expr(tc.expr, env).String(); got != tc.want {
			t.Errorf("%s: Expr = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestDecl(t *testing.T) {
	env := emptyEnv()
	cases := []struct {
		name string
		decl *ast.ConstDecl
		want string
	}{
		{"annotation wins", ast.NewConstDecl(nil, false, "X", ast.NewNamedType("int32", nil, nil), intLit("1"), nil), "int32"},
		{"unknown annotation", ast.NewConstDecl(nil, false, "X", ast.NewNamedType("notatype", nil, nil), intLit("1"), nil), "invalid"},
		{"inferred from value", ast.NewConstDecl(nil, false, "X", nil, intLit("1"), nil), "int"},
		{"no type, no value", ast.NewConstDecl(nil, false, "X", nil, nil, nil), "invalid"},
	}
	for _, tc := range cases {
		if got := Decl(tc.decl, env).String(); got != tc.want {
			t.Errorf("%s: Decl = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// report collects the calls Check makes, for asserting both count and detail.
type report struct {
	methods  []string
	operands []string
}

func (r *report) fn(_ ast.Node, method, operands string) {
	r.methods = append(r.methods, method)
	r.operands = append(r.operands, operands)
}

func TestCheckValid(t *testing.T) {
	env := emptyEnv()
	var r report
	got := Check(binary(intLit("1"), "add", intLit("2")), env, r.fn)
	if got.String() != "int" {
		t.Errorf("Check(1.add(2)) = %s, want int", got)
	}
	if len(r.methods) != 0 {
		t.Errorf("valid expression reported %v, want no reports", r.methods)
	}
}

func TestCheckReportsInvalid(t *testing.T) {
	env := emptyEnv()
	var r report
	// 1 && 2 desugars to (1).anan(2): a logical operator on integers.
	got := Check(binary(intLit("1"), "anan", intLit("2")), env, r.fn)
	if got != ir.Invalid {
		t.Errorf("Check = %s, want invalid", got)
	}
	if len(r.methods) != 1 || r.methods[0] != "anan" {
		t.Fatalf("methods = %v, want [anan]", r.methods)
	}
	if r.operands[0] != "int, int" {
		t.Errorf("operands = %q, want %q", r.operands[0], "int, int")
	}
}

func TestCheckReportsInnermostOnce(t *testing.T) {
	env := emptyEnv()
	var r report
	// 1 && 2 && 3: the inner error is reported once; the outer call sees an
	// Invalid operand and does not pile on.
	inner := binary(intLit("1"), "anan", intLit("2"))
	got := Check(binary(inner, "anan", intLit("3")), env, r.fn)
	if got != ir.Invalid {
		t.Errorf("Check = %s, want invalid", got)
	}
	if len(r.methods) != 1 {
		t.Errorf("reported %d times %v, want exactly one", len(r.methods), r.methods)
	}
}
