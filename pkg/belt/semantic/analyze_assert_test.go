// This file tests the assert declaration: the outcome of a folded assertion and
// the diagnostics a failed, non-bool, or unfoldable assertion records.
package semantic

import (
	"testing"
)

// --- assert declarations ------------------------------------------------------

func TestAssertPasses(t *testing.T) {
	_, diags := analyze("const Max = 100\nconst Min = 0\n" +
		"assert Max > Min\n" +
		"assert Min == 0\n" +
		"assert Max - Min == 100\n" +
		"assert !(Min > Max)\n" +
		"assert true\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestAssertFailed(t *testing.T) {
	_, diags := analyze("const Max = 100\nconst Min = 0\nassert Max < Min\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionFailed {
		t.Fatalf("codes = %v, want [assertion_failed]", got)
	}
	// The message summarizes the condition in surface syntax — rendered back
	// from the desugared AST — and draws the power-assert diagram beneath it,
	// every sub-expression's folded value under the place it appears.
	want := "assertion failed: Max < Min\n" +
		"  Max < Min\n" +
		"  ^   ^ ^\n" +
		"  100 | 0\n" +
		"      false"
	if diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestAssertNotBool(t *testing.T) {
	_, diags := analyze("const Max = 100\nassert Max + 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionNotBool {
		t.Fatalf("codes = %v, want [assertion_not_bool]", got)
	}
	if want := "assertion must be a bool; got nint"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestAssertUndefinedName(t *testing.T) {
	// An undefined reference is the existing diagnostic, once — not an extra
	// assertion_* on top of it.
	_, diags := analyze("assert missing > 0\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUndefinedName {
		t.Fatalf("codes = %v, want [undefined_name]", got)
	}
}

func TestAssertDivisionByZero(t *testing.T) {
	_, diags := analyze("assert 1 / 0 == 0\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeDivisionByZero {
		t.Fatalf("codes = %v, want [division_by_zero]", got)
	}
}

func TestAssertOperatorTypeError(t *testing.T) {
	_, diags := analyze("assert 1 && true\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("codes = %v, want [invalid_operation]", got)
	}
}

func TestAssertNotConstant(t *testing.T) {
	// A genuinely unfoldable assertion condition is the error — an assertion the
	// compiler cannot verify must not pass silently. A pure but unboundedly
	// recursive call types as bool yet the folder bottoms out at the depth guard,
	// so it never reaches a value.
	src := "fn loopy(n: nint): bool {\n  return loopy(n)\n}\nassert loopy(1)\n"
	_, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionNotConstant {
		t.Fatalf("codes = %v, want [assertion_not_constant]", got)
	}
}

func TestAssertListLenFolds(t *testing.T) {
	// list.len() now has a compile-time intrinsic (the foldable-completeness
	// work): the condition folds to true, so the assertion verifies clean. The
	// map.len() companion folds the same way.
	for _, src := range []string{"assert [1, 2, 3].len() == 3\n", "assert [\"a\": 1].len() == 1\n"} {
		if _, diags := analyze(src); len(codes(diags)) != 0 {
			t.Fatalf("%q: codes = %v, want no diagnostics (len folds)", src, codes(diags))
		}
	}
}

func TestAssertNominalMethodFolds(t *testing.T) {
	// A user-defined method on a nominal type over a primitive folds: the
	// receiver's static type is read from the constant's annotation (Level), the
	// method body is evaluated with self bound, and the assertion verifies clean.
	src := "type Level = sbyte impl {\n  increment(): self {\n    return self + 1\n  }\n}\n" +
		"const L: Level = 50\n" +
		"assert L.increment() == 51\n"
	_, diags := analyze(src)
	if got := codes(diags); len(got) != 0 {
		t.Fatalf("codes = %v, want no diagnostics (the assertion folds to true)", got)
	}
}

func TestAssertSelfNotConstant(t *testing.T) {
	// self has no referent at the top level: each occurrence is reported as
	// self_outside_method, which also explains why the assertion cannot fold —
	// so the generic assertion_not_constant stays suppressed.
	_, diags := analyze("assert self == self\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeSelfOutsideMethod || got[1] != CodeSelfOutsideMethod {
		t.Fatalf("codes = %v, want two self_outside_method", got)
	}
}

func TestAssertMissingExprIsTheParsersProblem(t *testing.T) {
	// A recovered "assert" without an expression already carries a parse
	// diagnostic; the semantic layer adds nothing.
	_, diags := analyze("assert\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestAssertOutcomeInModule(t *testing.T) {
	// An assert declares no value — the constants are untouched — but its
	// outcome (the folded condition and its power-assert diagram) is module
	// data, so hover and tooling read the very values it was checked with.
	with, _ := analyze("const A = 1\nassert A > 0\n")
	without, _ := analyze("const A = 1\n")
	if len(with.Consts) != len(without.Consts) || len(with.Types) != len(without.Types) {
		t.Errorf("assert changed the constants or types")
	}
	if len(with.Asserts) != 1 {
		t.Fatalf("got %d asserts in the module, want 1", len(with.Asserts))
	}
	a := with.Asserts[0]
	if a.Cond != "A > 0" || !a.Held() {
		t.Errorf("assert = {Cond: %q, Held: %v}, want A > 0 holding", a.Cond, a.Held())
	}
	want := "A > 0\n" +
		"^ ^\n" +
		"1 true"
	if a.Diagram != want {
		t.Errorf("diagram:\n%s\nwant:\n%s", a.Diagram, want)
	}
}

func TestAssertFailedWithDoc(t *testing.T) {
	// The doc comment is the invariant in the author's words: a failure
	// quotes it above the diagram.
	_, diags := analyze("const Max = 100\nconst Min = 0\n/// the maximum dominates\nassert Max < Min\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeAssertionFailed {
		t.Fatalf("codes = %v, want [assertion_failed]", got)
	}
	want := "assertion failed: Max < Min\n" +
		"  the maximum dominates\n" +
		"  Max < Min\n" +
		"  ^   ^ ^\n" +
		"  100 | 0\n" +
		"      false"
	if diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}
