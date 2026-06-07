package infer

// Tests for the reporting/checking walk (Check, CheckAgainst, checkType, and
// the func-literal/collection checking rules in check.go). Shared fixtures
// (ast/type builders, stubEnv, collectionEnv, report) live in infer_test.go.

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestCheckFuncLitBody(t *testing.T) {
	env := emptyEnv()
	x := param("x", namedType("nint"))

	// An operator error inside the body is reported.
	var r1 report
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(binary(ident("x"), "anan", intLit("1")))), env, r1.sink())
	if len(r1.methods) != 1 || r1.methods[0] != "anan" {
		t.Errorf("body operator error: methods = %v, want [anan]", r1.methods)
	}

	// A return that does not satisfy the declared result is a mismatch.
	var r2 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("bool"), ret(ident("x"))), env, r2.sink())
	if len(r2.mismatches) != 1 || r2.mismatches[0] != "nint -> bool" {
		t.Errorf("return mismatch: %v, want [nint -> bool]", r2.mismatches)
	}

	// Conflicting unannotated returns are reported at the later return.
	var r3 report
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(intLit("1")), ret(boolLit(true))), env, r3.sink())
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "bool -> nint" {
		t.Errorf("conflicting returns: %v, want [bool -> nint]", r3.mismatches)
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
	Check(funcLit([]*ast.ParamDef{x}, namedType("nint")), env, r5.sink())
	Check(funcLit([]*ast.ParamDef{x}, namedType("nint"), ret(ident("x"))), env, r5.sink())
	if r5.uninferables != 0 || len(r5.mismatches) != 0 || len(r5.methods) != 0 {
		t.Errorf("healthy literals reported %v %v %d", r5.methods, r5.mismatches, r5.uninferables)
	}

	// An invalid return value (an unresolved name) is not re-reported as a
	// mismatch — the undefined reference is some other check's finding.
	var r6 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("nint"), ret(ident("missing"))), env, r6.sink())
	if len(r6.mismatches) != 0 {
		t.Errorf("invalid return re-reported: %v", r6.mismatches)
	}

	// A bare expression statement's operator error is reported too.
	var r7 report
	Check(funcLit([]*ast.ParamDef{x}, namedType("nint"),
		ast.NewExprStmt(binary(ident("x"), "anan", ident("x")), nil),
		ret(ident("x")),
	), env, r7.sink())
	if len(r7.methods) != 1 || r7.methods[0] != "anan" {
		t.Errorf("expr-stmt operator error: methods = %v, want [anan]", r7.methods)
	}

	// An error inside a nested literal's body surfaces through the outer walk,
	// and only once.
	var r8 report
	inner := funcLit([]*ast.ParamDef{param("y", namedType("nint"))}, nil,
		ret(binary(ident("y"), "anan", intLit("1"))))
	Check(funcLit([]*ast.ParamDef{x}, nil, ret(inner)), env, r8.sink())
	if len(r8.methods) != 1 || r8.methods[0] != "anan" {
		t.Errorf("nested body operator error: methods = %v, want [anan]", r8.methods)
	}
	if len(r8.mismatches) != 0 {
		t.Errorf("nested invalid result re-reported: %v", r8.mismatches)
	}
}

// TestCheckFuncLitStatementBody covers the statement forms a lambda block body
// may carry beyond return/expr — let, assign, if, switch — which the body
// walkers must descend into so a nested return drives result inference and an
// error in a skipped statement is reported.
func TestCheckFuncLitStatementBody(t *testing.T) {
	env := emptyEnv()

	// 1. A type error in a let initializer inside a lambda body is reported.
	var r1 report
	lit1 := funcLit([]*ast.ParamDef{param("acc", namedType("nint")), param("value", namedType("nint"))},
		namedType("nint"),
		letStmt("bump", nil, binary(stringLit("str"), "add", intLit("1"))),
		ret(binary(ident("acc"), "add", ident("value"))),
	)
	Check(lit1, env, r1.sink())
	if len(r1.methods) != 1 || r1.methods[0] != "add" {
		t.Errorf("let-initializer error: methods = %v, want [add]", r1.methods)
	}

	// 2. A let local is in scope for the statements after it; a return that uses
	// it is checked against the result, and unifies for synthesis.
	var r2 report
	lit2 := funcLit([]*ast.ParamDef{param("value", namedType("nint"))}, nil,
		letStmt("bump", nil, binary(ident("value"), "add", intLit("1"))),
		ret(ident("bump")),
	)
	if got := Check(lit2, env, r2.sink()).String(); got != "fn(nint): nint" {
		t.Errorf("let local in scope: Check = %s, want fn(nint): nint", got)
	}
	if r2.uninferables != 0 || len(r2.methods) != 0 || len(r2.mismatches) != 0 {
		t.Errorf("healthy let body reported %v %v %d", r2.methods, r2.mismatches, r2.uninferables)
	}

	// 3. A return nested inside an if drives result synthesis: it must not be
	// reported as uninferable, and its type is the lambda's result.
	var r3 report
	lit3 := funcLit([]*ast.ParamDef{param("v", namedType("nint"))}, nil,
		ifStmt(binary(ident("v"), "gt", intLit("0")),
			[]ast.Stmt{ret(binary(ident("v"), "add", intLit("1")))}, nil, nil),
	)
	if got := Check(lit3, env, r3.sink()).String(); got != "fn(nint): nint" {
		t.Errorf("nested return synthesis: Check = %s, want fn(nint): nint", got)
	}
	if r3.uninferables != 0 {
		t.Errorf("nested return falsely uninferable %d times", r3.uninferables)
	}

	// 4. A return nested inside an if is checked against the declared result.
	var r4 report
	lit4 := funcLit([]*ast.ParamDef{param("v", namedType("nint"))}, namedType("nint"),
		ifStmt(binary(ident("v"), "gt", intLit("0")),
			[]ast.Stmt{ret(stringLit("oops"))}, nil, nil),
		ret(ident("v")),
	)
	Check(lit4, env, r4.sink())
	if len(r4.mismatches) != 1 || r4.mismatches[0] != "string -> nint" {
		t.Errorf("nested return mismatch: %v, want [string -> nint]", r4.mismatches)
	}

	// 5. An operator error in an if condition is reported.
	var r5 report
	lit5 := funcLit([]*ast.ParamDef{param("v", namedType("nint"))}, namedType("nint"),
		ifStmt(binary(ident("v"), "anan", intLit("1")),
			[]ast.Stmt{ret(ident("v"))}, nil, nil),
		ret(ident("v")),
	)
	Check(lit5, env, r5.sink())
	if len(r5.methods) != 1 || r5.methods[0] != "anan" {
		t.Errorf("if-condition error: methods = %v, want [anan]", r5.methods)
	}

	// 6. An assignment value's error is reported, and an assignment to a let
	// local does not disturb result inference.
	var r6 report
	lit6 := funcLit([]*ast.ParamDef{param("v", namedType("nint"))}, namedType("nint"),
		letStmt("acc", nil, intLit("0")),
		assignStmtT(ident("acc"), binary(stringLit("x"), "add", intLit("1"))),
		ret(ident("v")),
	)
	Check(lit6, env, r6.sink())
	if len(r6.methods) != 1 || r6.methods[0] != "add" {
		t.Errorf("assign-value error: methods = %v, want [add]", r6.methods)
	}
}

// --- checking mode (CheckAgainst) --------------------------------------------

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
		{"nint adapts", intLit("1"), builtinT("sbyte"), "nint", nil},
		{"same type", boolLit(true), builtinT("bool"), "bool", nil},
		{"scalar mismatch", intLit("1"), builtinT("bool"), "nint", []string{"nint -> bool"}},
		// Collection literals: the annotation reaches each entry.
		{"list adapts", listLit(intLit("1"), intLit("2")), nil, "list<sbyte>", nil},
		{"list entry mismatch", listLit(intLit("1"), boolLit(true)), nil, "list<sbyte>", []string{"bool -> sbyte"}},
		{"empty list takes want", listLit(), nil, "list<sbyte>", nil},
		{"non-collection want", listLit(intLit("1")), builtinT("nint"), "list<nint>", []string{"list<nint> -> nint"}},
		// Function literals: the expectation fills in what the literal omits.
		{
			"params and result pushed",
			funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(binary(ident("x"), "mul", intLit("2")))),
			fnT(builtinT("nint"), builtinT("nint")),
			"fn(nint): nint", nil,
		},
		{
			"annotation agrees and wins",
			funcLit([]*ast.ParamDef{param("x", namedType("nint"))}, nil, ret(binary(ident("x"), "mul", intLit("3")))),
			fnT(builtinT("nint"), builtinT("nint")),
			"fn(nint): nint", nil,
		},
		{
			"annotation conflicts",
			funcLit([]*ast.ParamDef{param("x", namedType("string"))}, nil, ret(ident("x"))),
			fnT(builtinT("nint"), builtinT("nint")),
			"fn(string): nint", []string{"string -> nint", "string -> nint"}, // the parameter and its return
		},
		{
			"result annotation conflicts",
			funcLit([]*ast.ParamDef{param("x", namedType("nint"))}, namedType("string"), ret(stringLit("s"))),
			fnT(builtinT("nint"), builtinT("nint")),
			"fn(nint): string", []string{"string -> nint"},
		},
		{
			"non-function want",
			funcLit(nil, namedType("nint"), ret(intLit("1"))),
			builtinT("nint"),
			"fn(): nint", []string{"fn(): nint -> nint"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == nil {
				want = listT(t, env, builtinT("sbyte"))
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
	want := mapT(t, env, builtinT("string"), builtinT("sbyte"))

	var r report
	got := CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), want, env, r.sink())
	if got.String() != "map<string, sbyte>" || len(r.mismatches) != 0 {
		t.Errorf("map literal = %s (mismatches %v), want map<string, sbyte>", got, r.mismatches)
	}

	// A key of the wrong type is reported at the key.
	var r2 report
	CheckAgainst(mapLit([2]ast.Expr{intLit("1"), intLit("2")}), want, env, r2.sink())
	if len(r2.mismatches) != 1 || r2.mismatches[0] != "nint -> string" {
		t.Errorf("map key mismatch = %v, want [nint -> string]", r2.mismatches)
	}

	// A map literal under a list expectation is a shape mismatch.
	var r3 report
	CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), listT(t, env, builtinT("nint")), env, r3.sink())
	if len(r3.mismatches) != 1 || r3.mismatches[0] != "map<string, nint> -> list<nint>" {
		t.Errorf("shape mismatch = %v", r3.mismatches)
	}
}

// TestCheckAgainstNominalCollection pins that a collection literal checked
// against a *nominal* list/map (type Names = list<string>) reaches the element
// types through the wrapper and adapts to it — the collection twin of a record
// literal checked against a nominal record. The literal takes the nominal type,
// a wrong element is reported, and the nominal wrapper still distinguishes a list
// expectation from a map one.
func TestCheckAgainstNominalCollection(t *testing.T) {
	env := collectionEnv()
	// type Names = list<string> and type Scores = map<string, sbyte>.
	names := &ir.Named{Def: &ir.TypeDef{Name: "Names", Body: listT(t, env, builtinT("string"))}}
	scores := &ir.Named{Def: &ir.TypeDef{Name: "Scores", Body: mapT(t, env, builtinT("string"), builtinT("sbyte"))}}

	// A matching list literal adapts to the nominal wrapper.
	var r report
	if got := CheckAgainst(listLit(stringLit("a"), stringLit("b")), names, env, r.sink()); got.String() != "Names" || len(r.mismatches) != 0 {
		t.Errorf("list into Names = %s (mismatches %v), want Names", got, r.mismatches)
	}
	// A wrong element is reported against the wrapped element type.
	var r2 report
	CheckAgainst(listLit(stringLit("a"), intLit("1")), names, env, r2.sink())
	if len(r2.mismatches) != 1 || r2.mismatches[0] != "nint -> string" {
		t.Errorf("Names element mismatch = %v, want [nint -> string]", r2.mismatches)
	}
	// A matching map literal adapts to the nominal map wrapper.
	var r3 report
	if got := CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), scores, env, r3.sink()); got.String() != "Scores" || len(r3.mismatches) != 0 {
		t.Errorf("map into Scores = %s (mismatches %v), want Scores", got, r3.mismatches)
	}
	// A map literal under a nominal list wrapper is still a shape mismatch.
	var r4 report
	CheckAgainst(mapLit([2]ast.Expr{stringLit("a"), intLit("1")}), names, env, r4.sink())
	if len(r4.mismatches) != 1 || r4.mismatches[0] != "map<string, nint> -> Names" {
		t.Errorf("shape mismatch into Names = %v, want [map<string, nint> -> Names]", r4.mismatches)
	}
}

func TestCheckAgainstArityAndInference(t *testing.T) {
	env := emptyEnv()
	intT := builtinT("nint")

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
	if got.String() != "fn(invalid): nint" {
		t.Errorf("uninferable parameter type = %s, want fn(invalid): nint", got)
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
	if got.String() != "fn(nint): bool" {
		t.Errorf("solved literal = %s, want fn(nint): bool", got)
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
	if got.String() != "fn(nint): invalid" {
		t.Errorf("unsolved literal = %s, want fn(nint): invalid", got)
	}
	if r4.uninferables != 1 {
		t.Errorf("uninferable result reported %d times, want 1", r4.uninferables)
	}
}

func TestCheckAgainstNested(t *testing.T) {
	env := collectionEnv()
	intT := builtinT("nint")

	// The expectation reaches a literal nested in a collection...
	var r report
	want := listT(t, env, fnT(intT, intT))
	got := CheckAgainst(listLit(funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(ident("x")))), want, env, r.sink())
	if got.String() != "list<fn(nint): nint>" || len(r.mismatches) != 0 {
		t.Errorf("nested in list = %s (mismatches %v)", got, r.mismatches)
	}

	// ...and one returned from another literal.
	var r2 report
	outer := funcLit(nil, nil, ret(funcLit([]*ast.ParamDef{param("x", nil)}, nil, ret(ident("x")))))
	got = CheckAgainst(outer, fnT(fnT(intT, intT)), env, r2.sink())
	if got.String() != "fn(): fn(nint): nint" || len(r2.mismatches) != 0 {
		t.Errorf("nested in literal = %s (mismatches %v)", got, r2.mismatches)
	}
}

func TestCheckValid(t *testing.T) {
	env := emptyEnv()
	var r report
	got := Check(binary(intLit("1"), "add", intLit("2")), env, r.sink())
	if got.String() != "nint" {
		t.Errorf("Check(1.add(2)) = %s, want nint", got)
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
	if r.operands[0] != "nint, nint" {
		t.Errorf("operands = %q, want %q", r.operands[0], "nint, nint")
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
