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

// --- function literals -------------------------------------------------------

func namedType(name string) *ast.NamedType { return ast.NewNamedType(name, nil, nil) }
func param(name string, typ ast.TypeExpr) *ast.ParamDef {
	return ast.NewParamDef(name, typ, nil)
}
func ret(value ast.Expr) ast.Stmt { return ast.NewReturnStmt(value, nil) }

// funcLit builds fn(params...) [: result] { body... } with a nil syntax node.
func funcLit(params []*ast.ParamDef, result ast.TypeExpr, body ...ast.Stmt) *ast.FuncLit {
	return ast.NewFuncLit(params, result, body, nil)
}

func TestFuncLitResultSynthesis(t *testing.T) {
	env := emptyEnv()
	x := param("x", namedType("int"))
	cases := []struct {
		name string
		lit  *ast.FuncLit
		want string
	}{
		{
			"declared result wins",
			funcLit([]*ast.ParamDef{x}, namedType("int"), ret(ident("x"))),
			"fn(int): int",
		},
		{
			"result from the body",
			funcLit([]*ast.ParamDef{x}, nil, ret(binary(ident("x"), "mul", intLit("2")))),
			"fn(int): int",
		},
		{
			"bool result from a comparison",
			funcLit([]*ast.ParamDef{x}, nil, ret(binary(ident("x"), "lt", intLit("0")))),
			"fn(int): bool",
		},
		{
			"two agreeing returns unify",
			funcLit([]*ast.ParamDef{x}, nil, ret(intLit("1")), ret(ident("x"))),
			"fn(int): int",
		},
		{
			"no return, no annotation",
			funcLit([]*ast.ParamDef{x}, nil),
			"fn(int): invalid",
		},
		{
			"conflicting returns",
			funcLit([]*ast.ParamDef{x}, nil, ret(intLit("1")), ret(boolLit(true))),
			"fn(int): invalid",
		},
		{
			"unannotated parameter stays invalid",
			funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(intLit("1"))),
			"fn(invalid): int",
		},
	}
	for _, tc := range cases {
		if got := Expr(tc.lit, env).String(); got != tc.want {
			t.Errorf("%s: Expr = %s, want %s", tc.name, got, tc.want)
		}
		// The checking walk types the literal identically (and a nil sink is
		// silent), so the pure and reporting walks cannot drift apart.
		if got := Check(tc.lit, env, nil).String(); got != tc.want {
			t.Errorf("%s: Check = %s, want %s", tc.name, got, tc.want)
		}
	}
}

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

func TestCheckFuncLitBody(t *testing.T) {
	env := emptyEnv()
	x := param("x", namedType("int"))

	// An operator error inside the body is reported.
	var r1 report
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(binary(ident("x"), "anan", intLit("1")))), env, r1.sink())
	if len(r1.methods) != 1 || r1.methods[0] != "anan" {
		t.Errorf("body operator error: methods = %v, want [anan]", r1.methods)
	}

	// A return that does not satisfy the declared result is a mismatch.
	var r2 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("bool"), ret(ident("x"))), env, r2.sink())
	if len(r2.mismatches) != 1 || r2.mismatches[0] != "int -> bool" {
		t.Errorf("return mismatch: %v, want [int -> bool]", r2.mismatches)
	}

	// Conflicting unannotated returns are reported at the later return.
	var r3 report
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(intLit("1")), ret(boolLit(true))), env, r3.sink())
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "bool -> int" {
		t.Errorf("conflicting returns: %v, want [bool -> int]", r3.mismatches)
	}

	// No annotation and no return: the result is uninferable.
	var r4 report
	Check(funcLit([]*ast.ParamDef{x}, nil), env, r4.sink())
	if r4.uninferables != 1 {
		t.Errorf("uninferable result reported %d times, want 1", r4.uninferables)
	}

	// A declared result with no return is not uninferable (the signature is
	// complete), and a healthy body reports nothing at all.
	var r5 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("int")), env, r5.sink())
	Check(funcLit([]*ast.ParamDef{x}, namedType("int"), ret(ident("x"))), env, r5.sink())
	if r5.uninferables != 0 || len(r5.mismatches) != 0 || len(r5.methods) != 0 {
		t.Errorf("healthy literals reported %v %v %d", r5.methods, r5.mismatches, r5.uninferables)
	}

	// An invalid return value (an unresolved name) is not re-reported as a
	// mismatch — the undefined reference is some other check's finding.
	var r6 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("int"), ret(ident("missing"))), env, r6.sink())
	if len(r6.mismatches) != 0 {
		t.Errorf("invalid return re-reported: %v", r6.mismatches)
	}

	// A bare expression statement's operator error is reported too.
	var r7 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("int"),
		ast.NewExprStmt(binary(ident("x"), "anan", ident("x")), nil),
		ret(ident("x")),
	), env, r7.sink())
	if len(r7.methods) != 1 || r7.methods[0] != "anan" {
		t.Errorf("expr-stmt operator error: methods = %v, want [anan]", r7.methods)
	}

	// An error inside a nested literal's body surfaces through the outer walk,
	// and only once.
	var r8 report
	inner := funcLit([]*ast.ParamDef{param("y", namedType("int"))}, nil,
		ret(binary(ident("y"), "anan", intLit("1"))))
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(inner)), env, r8.sink())
	if len(r8.methods) != 1 || r8.methods[0] != "anan" {
		t.Errorf("nested body operator error: methods = %v, want [anan]", r8.methods)
	}
	if len(r8.mismatches) != 0 {
		t.Errorf("nested invalid result re-reported: %v", r8.mismatches)
	}
}

// report collects the findings Check sinks, for asserting both count and
// detail.
type report struct {
	methods      []string
	operands     []string
	mismatches   []string // rendered "got -> want"
	uninferables int
}

func (r *report) sink() *Sink {
	return &Sink{
		InvalidOp: func(_ ast.Node, method, operands string) {
			r.methods = append(r.methods, method)
			r.operands = append(r.operands, operands)
		},
		Mismatch: func(_ ast.Expr, got, want ir.Type) {
			r.mismatches = append(r.mismatches, got.String()+" -> "+want.String())
		},
		UninferableResult: func(*ast.FuncLit) { r.uninferables++ },
	}
}

func TestCheckValid(t *testing.T) {
	env := emptyEnv()
	var r report
	got := Check(binary(intLit("1"), "add", intLit("2")), env, r.sink())
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
	got := Check(binary(intLit("1"), "anan", intLit("2")), env, r.sink())
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
	got := Check(binary(inner, "anan", intLit("3")), env, r.sink())
	if got != ir.Invalid {
		t.Errorf("Check = %s, want invalid", got)
	}
	if len(r.methods) != 1 {
		t.Errorf("reported %d times %v, want exactly one", len(r.methods), r.methods)
	}
}
