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
