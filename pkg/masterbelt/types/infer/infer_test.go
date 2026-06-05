package infer

import (
	"fmt"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
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

func (e stubEnv) Resolve(id *ast.Identifier) *ast.ConstDecl           { return e.res[id] }
func (e stubEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl      { return nil }
func (e stubEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl      { return nil }
func (e stubEnv) ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl { return nil }
func (e stubEnv) TypeOf(decl *ast.ConstDecl) ir.Type                  { return e.typ[decl] }

// Universe mirrors what the semantic layer feeds the resolver: the prelude
// surface (here, the registry's definitions) beneath any declared types.
func (e stubEnv) Universe() map[string]*ir.TypeDef {
	out := map[string]*ir.TypeDef{}
	for _, d := range e.reg.Defs() {
		out[d.Name] = d
	}
	return out
}
func (e stubEnv) QualifiedType(namespace, name string) *ir.TypeDef { return nil }
func (e stubEnv) Registry() *builtin.Registry                      { return e.reg }

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
		{"annotation wins", ast.NewConstDecl(nil, false, "X", ast.NewNamedType("", "int32", nil, nil), intLit("1"), nil), "int32"},
		{"unknown annotation", ast.NewConstDecl(nil, false, "X", ast.NewNamedType("", "notatype", nil, nil), intLit("1"), nil), "invalid"},
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

func namedType(name string) *ast.NamedType { return ast.NewNamedType("", name, nil, nil) }
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

// --- shared fixtures for the checking and call tests -------------------------

func stringLit(value string) *ast.StringLit { return ast.NewStringLit(value, nil) }

func listLit(values ...ast.Expr) *ast.CollectionLit {
	entries := make([]*ast.CollectionEntry, len(values))
	for i, v := range values {
		entries[i] = &ast.CollectionEntry{Value: v}
	}
	return ast.NewCollectionLit(entries, nil)
}

func mapLit(pairs ...[2]ast.Expr) *ast.CollectionLit {
	entries := make([]*ast.CollectionEntry, len(pairs))
	for i, p := range pairs {
		entries[i] = &ast.CollectionEntry{Key: p[0], Value: p[1]}
	}
	return ast.NewCollectionLit(entries, nil)
}

// builtinT and the type builders construct expected types directly — the want
// side of checking is an ir.Type, not syntax.
func builtinT(name string) ir.Type { return &ir.Builtin{Name: name} }
func fnT(result ir.Type, params ...ir.Type) *ir.Func {
	return &ir.Func{Params: params, Result: result}
}

// collectionEnv is emptyEnv with synthetic list and map definitions installed —
// the prelude (which declares the real ones) is not available to this package.
func collectionEnv() stubEnv {
	env := emptyEnv()
	env.reg.Install([]*ir.TypeDef{
		{Name: "list", Builtin: true, Params: []*ir.TypeParam{{Name: "T"}}},
		{Name: "map", Builtin: true, Params: []*ir.TypeParam{{Name: "K"}, {Name: "V"}}},
	})
	return env
}

// listT resolves list<elem> through the registry, so the App's Def is the one
// collection rules compare against.
func listT(t *testing.T, env stubEnv, elem ir.Type) ir.Type {
	t.Helper()
	def, ok := env.reg.Lookup("list")
	if !ok {
		t.Fatal("no list in the registry")
	}
	return &ir.App{Def: def, Args: []ir.Type{elem}}
}

func mapT(t *testing.T, env stubEnv, key, value ir.Type) ir.Type {
	t.Helper()
	def, ok := env.reg.Lookup("map")
	if !ok {
		t.Fatal("no map in the registry")
	}
	return &ir.App{Def: def, Args: []ir.Type{key, value}}
}

// report collects the findings Check sinks, for asserting both count and
// detail.
type report struct {
	methods           []string
	operands          []string
	mismatches        []string // rendered "got -> want"
	arities           []string // rendered "got of want"
	uninferableParams []string // parameter names
	uninferables      int      // uninferable results
}

func (r *report) sink() *Sink {
	return &Sink{
		InvalidOp: func(_ ast.Node, method, operands string) {
			r.methods = append(r.methods, method)
			r.operands = append(r.operands, operands)
		},
		Mismatch: func(_ ast.Node, got, want ir.Type) {
			r.mismatches = append(r.mismatches, got.String()+" -> "+want.String())
		},
		ArityMismatch: func(_ *ast.FuncLit, got, want int) {
			r.arities = append(r.arities, fmt.Sprintf("%d of %d", got, want))
		},
		UninferableParam: func(p *ast.ParamDef) {
			r.uninferableParams = append(r.uninferableParams, p.Name)
		},
		UninferableResult: func(*ast.FuncLit) { r.uninferables++ },
	}
}
