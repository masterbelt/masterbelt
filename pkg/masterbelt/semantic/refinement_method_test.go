package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

// These tests pin stream B of the folding-completeness work: a where-clause
// predicate may call a method of the type being refined (where self.isValid()),
// and that call both type-checks — self is the nominal type, so its own methods
// resolve — and folds at compile time, gating annotated constants.

// TestWhereSelfMethodSatisfied checks that a predicate calling a self method
// type-checks and rides the definition, and a constant that satisfies it passes.
func TestWhereSelfMethodSatisfied(t *testing.T) {
	src := "pub type Lvl = int8 where self.isValid() impl {\n" +
		"  pub isValid(): bool {\n    return self >= 0 && self <= 100\n  }\n}\n" +
		"const Ok: Lvl = 50\n"
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 0 {
		t.Fatalf("codes = %v, want no diagnostics", got)
	}
	if m.Types[0].Where == nil {
		t.Error("Lvl.Where = nil, want the validated self-method predicate")
	}
}

// TestWhereSelfMethodViolation checks that a constant the self-method predicate
// rejects is reported as a refinement violation — the predicate folds to false.
func TestWhereSelfMethodViolation(t *testing.T) {
	src := "pub type Lvl = int8 where self.isValid() impl {\n" +
		"  pub isValid(): bool {\n    return self >= 0 && self <= 100\n  }\n}\n" +
		"const Bad: Lvl = -5\n"
	if got := codes(diagsOf(t, src)); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
}

// TestWhereSelfMethodComposed checks a predicate that mixes a self-method call
// with a direct comparison: both must fold for the conjunction to.
func TestWhereSelfMethodComposed(t *testing.T) {
	src := "pub type Lvl = int8 where self.positive() && self <= 100 impl {\n" +
		"  pub positive(): bool {\n    return self > 0\n  }\n}\n" +
		"const Ok: Lvl = 10\n"
	if got := codes(diagsOf(t, src)); len(got) != 0 {
		t.Fatalf("codes = %v, want no diagnostics", got)
	}
	bad := "pub type Lvl = int8 where self.positive() && self <= 100 impl {\n" +
		"  pub positive(): bool {\n    return self > 0\n  }\n}\n" +
		"const Bad: Lvl = 0\n"
	if got := codes(diagsOf(t, bad)); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
}

// TestWhereSelfMethodCallsMethod checks a self method whose body calls another
// self method: the predicate fold descends through both bodies.
func TestWhereSelfMethodCallsMethod(t *testing.T) {
	src := "pub type Lvl = int8 where self.ok() impl {\n" +
		"  pub positive(): bool {\n    return self > 0\n  }\n" +
		"  pub ok(): bool {\n    return self.positive()\n  }\n}\n" +
		"const Good: Lvl = 3\n"
	if got := codes(diagsOf(t, src)); len(got) != 0 {
		t.Fatalf("codes = %v, want no diagnostics", got)
	}
}

// TestWhereSelfMethodRecursionGuarded checks the depth guard: a where-clause
// whose self method recurses without bottoming out does not hang — the predicate
// is reported as not a compile-time predicate, once, at the declaration.
func TestWhereSelfMethodRecursionGuarded(t *testing.T) {
	src := "pub type Lvl = int8 where self.check() impl {\n" +
		"  pub check(): bool {\n    return self.check()\n  }\n}\n" +
		"const c: Lvl = 1\n"
	if got := codes(diagsOf(t, src)); len(got) != 1 || got[0] != CodeRefinementNotConstant {
		t.Fatalf("codes = %v, want [refinement_not_constant] (the depth guard fired), got %v", got, got)
	}
}

// diagsOf analyzes src and returns its diagnostics.
func diagsOf(t *testing.T, src string) []diagnostic.Diagnostic {
	t.Helper()
	_, diags := analyze(src)
	return diags
}
