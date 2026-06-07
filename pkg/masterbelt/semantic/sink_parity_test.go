// This file pins the sink-hook parity work: a diagnostic that fires in a const
// initializer must also fire wherever the same value-checking walk descends — in
// a lambda passed as a call argument (the observe() wrapper path) and in every
// function/method/lambda body (the bodySink path). The defect class was a sink
// wrapper or body sink that carried a stale subset of the checking walk's
// callbacks, so a ternary type error or a sized-conversion overflow went silent
// on one path while firing on another.
package semantic

import "testing"

// --- Finding 1: observe() must forward the ternary callbacks --------------
// A lambda passed as a call argument is checked through observe(), which used to
// drop TernaryCondNotBool and TernaryBranchMismatch.

// TestLambdaArgTernaryConditionNotBool checks a non-bool ternary condition inside
// a lambda passed to fold is reported — the same diagnostic a top-level const
// emits for the same expression.
func TestLambdaArgTernaryConditionNotBool(t *testing.T) {
	src := "const xs = [1, 2]\nconst X = xs.fold(0, fn(acc, k, v) -> 5 ? acc : acc)\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeTernaryConditionNotBool) {
		t.Fatalf("lambda-arg non-bool ternary condition: want ternary_condition_not_bool, got %v", codes(diags))
	}
}

// TestLambdaArgTernaryBranchMismatch checks two non-unifying ternary branches
// inside a lambda passed to fold are reported. Under an expectation (the
// solved lambda result drives both branches) the precise per-branch
// type_mismatch is the diagnostic — the offending branch named, no pile-on
// ternary_branch_mismatch — while the synthesis form (no expectation, pinned
// below) keeps ternary_branch_mismatch.
func TestLambdaArgTernaryBranchMismatch(t *testing.T) {
	src := "const xs = [1, 2]\nconst X = xs.fold(0, fn(acc, k, v) -> true ? 1 : \"s\")\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeTypeMismatch) {
		t.Fatalf("lambda-arg ternary branch mismatch: want type_mismatch at the branch, got %v", codes(diags))
	}
}

// TestBareTernaryBranchMismatch pins the synthesis form: with no expectation
// to drive the branches, two non-unifying branch types are the ternary's own
// finding, ternary_branch_mismatch.
func TestBareTernaryBranchMismatch(t *testing.T) {
	src := "const X = true ? 1 : \"s\"\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeTernaryBranchMismatch) {
		t.Fatalf("bare ternary branch mismatch: want ternary_branch_mismatch, got %v", codes(diags))
	}
}

// --- Finding 2: observe() must forward ScalarConversion -------------------

// TestLambdaArgScalarConversionOverflow checks an out-of-range sized-scalar
// conversion inside a lambda passed to fold is reported as constant_overflow.
// acc is short, so short(70000) fits the type but overflows the value, and only
// the ScalarConversion hook range-checks the conversion's argument.
func TestLambdaArgScalarConversionOverflow(t *testing.T) {
	src := "const xs = [1, 2]\nconst X = xs.fold(short(0), fn(acc, k, v) -> short(70000))\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeConstantOverflow) {
		t.Fatalf("lambda-arg scalar conversion overflow: want constant_overflow, got %v", codes(diags))
	}
}

// --- Finding 3: bodySink must wire ScalarConversion -----------------------
// The Checked stream was wired into bodySink earlier; the ScalarConversion hook
// was not, so a sized conversion overflow stayed silent in every body.

// TestFuncBodyScalarConversionOverflow checks an overflowing conversion in a
// function body is reported, matching `const A: short = short(70000)`.
func TestFuncBodyScalarConversionOverflow(t *testing.T) {
	src := "fn f(): short {\n  return short(70000)\n}\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeConstantOverflow) {
		t.Fatalf("func body scalar conversion overflow: want constant_overflow, got %v", codes(diags))
	}
}

// TestFuncBodyNominalScalarConversionOverflow checks a conversion to a nominal
// type over a sized scalar overflows the same way in a body.
func TestFuncBodyNominalScalarConversionOverflow(t *testing.T) {
	src := "type Level = short\nfn f(): Level {\n  return Level(70000)\n}\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeConstantOverflow) {
		t.Fatalf("func body nominal scalar conversion overflow: want constant_overflow, got %v", codes(diags))
	}
}

// TestMethodBodyScalarConversionOverflow checks an overflowing conversion in a
// method body is reported.
func TestMethodBodyScalarConversionOverflow(t *testing.T) {
	src := "pub type Lvl = sbyte impl {\n  pub big(): short { return short(70000) }\n}\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeConstantOverflow) {
		t.Fatalf("method body scalar conversion overflow: want constant_overflow, got %v", codes(diags))
	}
}

// TestFuncBodyCollectionEntryOverflow checks an out-of-range constant in a
// collection entry of a function body is reported through the Checked stream,
// matching `const X: list<short> = [70000]`.
func TestFuncBodyCollectionEntryOverflow(t *testing.T) {
	src := "fn f(): list<short> {\n  return [70000]\n}\n"
	if _, diags := analyze(src); !hasCodeSwitch(diags, CodeConstantOverflow) {
		t.Fatalf("func body collection entry overflow: want constant_overflow, got %v", codes(diags))
	}
}

// --- in-range controls ----------------------------------------------------
// The same shapes with in-range values and well-typed ternaries must stay clean,
// so the parity hooks do not over-report.

// TestSinkParityCleanCases checks the parity hooks do not fire on sound code:
// a bool-conditioned ternary with unifying branches in a lambda argument, an
// in-range conversion in a body, and an in-range collection entry in a body.
func TestSinkParityCleanCases(t *testing.T) {
	clean := []string{
		"const xs = [1, 2]\nconst X = xs.fold(0, fn(acc, k, v) -> v > 0 ? acc : acc)\n",
		"const xs = [1, 2]\nconst X = xs.fold(short(0), fn(acc, k, v) -> short(100))\n",
		"fn f(): short {\n  return short(100)\n}\n",
		"fn f(): list<short> {\n  return [100]\n}\n",
	}
	for _, src := range clean {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("clean parity case unexpectedly diagnosed: %q -> %v", src, codes(diags))
		}
	}
}
