package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// A function-literal block body is the same statement grammar a method body is —
// parseFuncLit parses a block, lowerFuncLit lowers it with lowerBlock — so a
// lambda body may carry let/assign/if/switch, not just return/expr. These tests
// pin that those statements are lowered into the literal's IR body (rather than
// silently dropped, leaving a <none> value graph that diverges from eval) and
// that the statement forms beyond return/expr are type-checked.

// constLambdaBody returns the lowered IR body of the function literal that is
// the single argument of the call initializing the named constant
// (const X = recv.method(fn(...) { ... })). It is the shape the lowering
// findings observed corrupt.
func constLambdaBody(t *testing.T, m *ir.Module, name string) []ir.Stmt {
	t.Helper()
	for _, c := range m.Consts {
		if c.Name != name {
			continue
		}
		call, ok := c.Value.(*ir.Call)
		if !ok {
			t.Fatalf("const %s value = %T, want *ir.Call", name, c.Value)
		}
		for _, a := range call.Args {
			if lit, ok := a.(*ir.FuncLiteral); ok {
				return lit.Body
			}
		}
		t.Fatalf("const %s call has no function-literal argument", name)
	}
	t.Fatalf("const %s not found", name)
	return nil
}

// TestLambdaBodyLowersLet checks that a let inside a lambda body is lowered into
// the literal's IR — with a following reference resolving to an ir.LocalRef,
// not the <none> the dropped-let path produced.
func TestLambdaBodyLowersLet(t *testing.T) {
	src := "const xs = [1, 2, 3]\n" +
		"const total = xs.fold(0, fn(acc, key, value) {\n" +
		"  let bump = value + 1\n" +
		"  return acc + bump\n" +
		"})\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	body := constLambdaBody(t, m, "total")
	if len(body) != 2 {
		t.Fatalf("lambda body has %d statements, want 2 (the let and the return)", len(body))
	}
	let, ok := body[0].(*ir.Let)
	if !ok || let.Name != "bump" {
		t.Fatalf("stmt 0 = %v, want let bump", body[0])
	}
	ret, ok := body[1].(*ir.Return)
	if !ok {
		t.Fatalf("stmt 1 = %T, want *ir.Return", body[1])
	}
	// The return is acc + bump = ParamRef("acc").add(LocalRef("bump")); the let
	// local must resolve to a LocalRef, never <none>.
	add, ok := ret.Value.(*ir.Call)
	if !ok || add.Method != "add" || len(add.Args) != 1 {
		t.Fatalf("return value = %v, want acc.add(bump)", ret.Value)
	}
	if local, ok := add.Args[0].(*ir.LocalRef); !ok || local.Name != "bump" {
		t.Errorf("return argument = %v, want LocalRef \"bump\" (the let was dropped)", add.Args[0])
	}
}

// TestLambdaBodyLowersSwitch checks that a switch on a let-bound local inside a
// lambda body lowers with the scrutinee resolved to that local (not <none>) and
// its bare-member arms resolved through the local's enum — the garbage-switch
// the dropped-let path produced.
func TestLambdaBodyLowersSwitch(t *testing.T) {
	src := "pub enum Rarity { Common, Rare }\n" +
		"const xs = [1]\n" +
		"const r = xs.map(fn(v) {\n" +
		"  let g: Rarity = Common\n" +
		"  switch g {\n" +
		"    Common -> return v\n" +
		"    Rare -> return v + 1\n" +
		"  }\n" +
		"})\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	body := constLambdaBody(t, m, "r")
	if len(body) != 2 {
		t.Fatalf("lambda body has %d statements, want 2 (the let and the switch)", len(body))
	}
	if let, ok := body[0].(*ir.Let); !ok || let.Name != "g" {
		t.Fatalf("stmt 0 = %v, want let g", body[0])
	}
	sw, ok := body[1].(*ir.Switch)
	if !ok {
		t.Fatalf("stmt 1 = %T, want *ir.Switch", body[1])
	}
	if local, ok := sw.Scrutinee.(*ir.LocalRef); !ok || local.Name != "g" {
		t.Fatalf("switch scrutinee = %v, want LocalRef \"g\" (the let was dropped)", sw.Scrutinee)
	}
	if len(sw.Arms) != 2 {
		t.Fatalf("switch has %d arms, want 2", len(sw.Arms))
	}
	for i, arm := range sw.Arms {
		if len(arm.Values) != 1 {
			t.Fatalf("arm %d has %d values, want 1", i, len(arm.Values))
		}
		if _, ok := arm.Values[0].(*ir.EnumMemberValue); !ok {
			t.Errorf("arm %d value = %v, want an enum-member value (bare member unresolved)", i, arm.Values[0])
		}
	}
}

// TestLambdaBodyLowersAssign checks that an assignment to a let local inside a
// lambda body is lowered into the literal's IR rather than dropped.
func TestLambdaBodyLowersAssign(t *testing.T) {
	src := "const xs = [1, 2, 3]\n" +
		"const total = xs.fold(0, fn(acc, key, value) {\n" +
		"  let n = value\n" +
		"  n = n + 1\n" +
		"  return acc + n\n" +
		"})\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	body := constLambdaBody(t, m, "total")
	if len(body) != 3 {
		t.Fatalf("lambda body has %d statements, want 3 (let, assign, return)", len(body))
	}
	if assign, ok := body[1].(*ir.Assign); !ok || assign.Name != "n" {
		t.Fatalf("stmt 1 = %v, want assign n", body[1])
	}
}

// TestLambdaBodyTypeChecksLetValue checks that a type error inside a let
// initializer in a lambda body is reported — the return-only walker skipped it.
func TestLambdaBodyTypeChecksLetValue(t *testing.T) {
	src := "const xs = [1, 2, 3]\n" +
		"const total = xs.fold(0, fn(acc, key, value) {\n" +
		"  let bad = \"str\" + 1\n" +
		"  return acc + value\n" +
		"})\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeNoMatchingOverload) && !hasCode(diags, CodeInvalidOperation) {
		t.Fatalf("want an operator diagnostic for \"str\" + 1 inside a lambda let, got %v", codes(diags))
	}
}

// TestLambdaBodyTypeChecksNestedReturn checks that a return nested in an if
// inside a lambda body drives result inference (no false uninferable_result) and
// its value participates in the type — and that the program folds, so the value
// query and the type query agree.
func TestLambdaBodyTypeChecksNestedReturn(t *testing.T) {
	src := "const r = [1, 2, 3].map(fn(v) {\n" +
		"  if v > 0 {\n" +
		"    return v + 1\n" +
		"  }\n" +
		"  return v\n" +
		"})\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a nested return should infer the result, got %v", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Name == "r" {
			if c.Type.String() != "list<int>" {
				t.Errorf("r type = %s, want list<int>", c.Type)
			}
			if c.Eval == nil || c.Eval.String() != "[2, 3, 4]" {
				t.Errorf("r eval = %v, want [2, 3, 4] (type/value divergence)", c.Eval)
			}
		}
	}
}

// TestLambdaBodyTypeChecksNestedReturnMismatch checks that a return nested in an
// if inside a lambda body is checked against the declared result type.
func TestLambdaBodyTypeChecksNestedReturnMismatch(t *testing.T) {
	src := "const f: fn(v: int): int = fn(v) {\n" +
		"  if v > 0 {\n" +
		"    return \"oops\"\n" +
		"  }\n" +
		"  return v\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch for a string return where int is declared, got %v", codes(diags))
	}
}

// TestLambdaBodyLetAndEvalAgree checks the lowering↔eval agreement the findings
// flagged: a lambda whose body uses a let folds to the same value the lowered
// graph describes (here through a foldable map, which folds at compile time).
func TestLambdaBodyLetAndEvalAgree(t *testing.T) {
	src := "const r = [1, 2, 3].map(fn(v) {\n" +
		"  let doubled = v * 2\n" +
		"  return doubled + 1\n" +
		"})\n"
	got := evalOf(t, src, "r")
	if got.String() != "[3, 5, 7]" {
		t.Errorf("r eval = %s, want [3, 5, 7]", got.String())
	}
}
