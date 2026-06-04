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

func (e stubEnv) Resolve(id *ast.Identifier) *ast.ConstDecl      { return e.res[id] }
func (e stubEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl { return nil }
func (e stubEnv) TypeOf(decl *ast.ConstDecl) ir.Type             { return e.typ[decl] }
func (e stubEnv) Universe() map[string]*ir.TypeDef               { return nil }
func (e stubEnv) Registry() *builtin.Registry                    { return e.reg }

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

// --- checking mode (CheckAgainst) --------------------------------------------

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

// TestCheckAgainst covers the checking rules (form × want): push-down into
// function and collection literals, synthesis plus subsumption for everything
// else.
func TestCheckAgainst(t *testing.T) {
	env := collectionEnv()
	cases := []struct {
		name       string
		expr       ast.Expr
		want       ir.Type
		typ        string   // the returned type
		mismatches []string // expected Mismatch reports, "got -> want"
	}{
		// Synthesis + subsumption.
		{"int adapts", intLit("1"), builtinT("int8"), "int", nil},
		{"same type", boolLit(true), builtinT("bool"), "bool", nil},
		{"scalar mismatch", intLit("1"), builtinT("bool"), "int", []string{"int -> bool"}},
		// Collection literals: the annotation reaches each entry.
		{"list adapts", listLit(intLit("1"), intLit("2")), nil, "list<int8>", nil},
		{"list entry mismatch", listLit(intLit("1"), boolLit(true)), nil, "list<int8>", []string{"bool -> int8"}},
		{"empty list takes want", listLit(), nil, "list<int8>", nil},
		{"non-collection want", listLit(intLit("1")), builtinT("int"), "list<int>", []string{"list<int> -> int"}},
		// Function literals: the expectation fills in what the literal omits.
		{
			"params and result pushed",
			funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(binary(ident("x"), "mul", intLit("2")))),
			fnT(builtinT("int"), builtinT("int")),
			"fn(int): int", nil,
		},
		{
			"annotation agrees and wins",
			funcLit([]*ast.ParamDef{param("x", namedType("int"))}, nil, ret(binary(ident("x"), "mul", intLit("3")))),
			fnT(builtinT("int"), builtinT("int")),
			"fn(int): int", nil,
		},
		{
			"annotation conflicts",
			funcLit([]*ast.ParamDef{param("x", namedType("string"))}, nil, ret(ident("x"))),
			fnT(builtinT("int"), builtinT("int")),
			"fn(string): int", []string{"string -> int", "string -> int"}, // the parameter and its return
		},
		{
			"result annotation conflicts",
			funcLit([]*ast.ParamDef{param("x", namedType("int"))}, namedType("string"), ret(stringLit("s"))),
			fnT(builtinT("int"), builtinT("int")),
			"fn(int): string", []string{"string -> int"},
		},
		{
			"non-function want",
			funcLit(nil, namedType("int"), ret(intLit("1"))),
			builtinT("int"),
			"fn(): int", []string{"fn(): int -> int"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == nil {
				want = listT(t, env, builtinT("int8"))
			}
			var r report
			got := CheckAgainst(tc.expr, want, env, r.sink())
			if got.String() != tc.typ {
				t.Errorf("CheckAgainst = %s, want %s", got, tc.typ)
			}
			if len(r.mismatches) != len(tc.mismatches) {
				t.Fatalf("mismatches = %v, want %v", r.mismatches, tc.mismatches)
			}
			for i := range r.mismatches {
				if r.mismatches[i] != tc.mismatches[i] {
					t.Errorf("mismatch %d = %s, want %s", i, r.mismatches[i], tc.mismatches[i])
				}
			}
		})
	}
}

func TestCheckAgainstMapLiteral(t *testing.T) {
	env := collectionEnv()
	want := mapT(t, env, builtinT("string"), builtinT("int8"))

	var r report
	got := CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), want, env, r.sink())
	if got.String() != "map<string, int8>" || len(r.mismatches) != 0 {
		t.Errorf("map literal = %s (mismatches %v), want map<string, int8>", got, r.mismatches)
	}

	// A key of the wrong type is reported at the key.
	var r2 report
	CheckAgainst(mapLit([2]ast.Expr{intLit("1"), intLit("2")}), want, env, r2.sink())
	if len(r2.mismatches) != 1 || r2.mismatches[0] != "int -> string" {
		t.Errorf("map key mismatch = %v, want [int -> string]", r2.mismatches)
	}

	// A map literal under a list expectation is a shape mismatch.
	var r3 report
	CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), listT(t, env, builtinT("int")), env, r3.sink())
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "map<string, int> -> list<int>" {
		t.Errorf("shape mismatch = %v", r3.mismatches)
	}
}

func TestCheckAgainstArityAndInference(t *testing.T) {
	env := emptyEnv()
	intT := builtinT("int")

	// Too many parameters: reported, the type is Invalid.
	var r report
	got := CheckAgainst(
		funcLit([]*ast.ParamDef{param("x", nil), param("y", nil)}, nil, ret(ident("x"))),
		fnT(intT, intT), env, r.sink())
	if got != ir.Invalid {
		t.Errorf("arity mismatch type = %s, want invalid", got)
	}
	if len(r.arities) != 1 || r.arities[0] != "2 of 1" {
		t.Errorf("arities = %v, want [2 of 1]", r.arities)
	}

	// A parameter the context cannot pin (an unbound variable) is reported.
	var r2 report
	got = CheckAgainst(
		funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(ident("x"))),
		fnT(intT, &ir.TypeVar{Name: "T"}), env, r2.sink())
	if got.String() != "fn(invalid): int" {
		t.Errorf("uninferable parameter type = %s, want fn(invalid): int", got)
	}
	if len(r2.uninferableParams) != 1 || r2.uninferableParams[0] != "x" {
		t.Errorf("uninferable params = %v, want [x]", r2.uninferableParams)
	}

	// An unbound result variable (map's R) is solved from the body.
	var r3 report
	subst := map[string]ir.Type{}
	got = checkType(
		funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(binary(ident("x"), "lt", intLit("0")))),
		fnT(&ir.TypeVar{Name: "R"}, intT), constScope{env}, subst, r3.sink())
	if got.String() != "fn(int): bool" {
		t.Errorf("solved literal = %s, want fn(int): bool", got)
	}
	if subst["R"] == nil || subst["R"].String() != "bool" {
		t.Errorf("subst[R] = %v, want bool", subst["R"])
	}
	if len(r3.mismatches) != 0 || r3.uninferables != 0 {
		t.Errorf("unexpected reports: %v / %d", r3.mismatches, r3.uninferables)
	}

	// No return to solve the variable from: uninferable result.
	var r4 report
	got = checkType(
		funcLit([]*ast.ParamDef{param("x", nil)}, nil),
		fnT(&ir.TypeVar{Name: "R"}, intT), constScope{env}, map[string]ir.Type{}, r4.sink())
	if got.String() != "fn(int): invalid" {
		t.Errorf("unsolved literal = %s, want fn(int): invalid", got)
	}
	if r4.uninferables != 1 {
		t.Errorf("uninferable result reported %d times, want 1", r4.uninferables)
	}
}

func TestCheckAgainstNested(t *testing.T) {
	env := collectionEnv()
	intT := builtinT("int")

	// The expectation reaches a literal nested in a collection...
	var r report
	want := listT(t, env, fnT(intT, intT))
	got := CheckAgainst(listLit(funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(ident("x")))), want, env, r.sink())
	if got.String() != "list<fn(int): int>" || len(r.mismatches) != 0 {
		t.Errorf("nested in list = %s (mismatches %v)", got, r.mismatches)
	}

	// ...and one returned from another literal.
	var r2 report
	outer := funcLit(nil, nil, ret(funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(ident("x")))))
	got = CheckAgainst(outer, fnT(fnT(intT, intT)), env, r2.sink())
	if got.String() != "fn(): fn(int): int" || len(r2.mismatches) != 0 {
		t.Errorf("nested in literal = %s (mismatches %v)", got, r2.mismatches)
	}
}

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
