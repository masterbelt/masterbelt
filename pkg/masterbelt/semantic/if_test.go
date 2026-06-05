package semantic

import (
	"testing"
)

// TestIfConditionNotBool checks that a non-bool if condition is reported as
// condition_not_bool.
func TestIfConditionNotBool(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  if n {\n    return 1\n  }\n  return 0\n}\n")
	if !hasCodeSwitch(diags, CodeConditionNotBool) {
		t.Fatalf("want condition_not_bool, got %v", codes(diags))
	}
}

// TestIfConditionBoolOK checks that a bool condition (a comparison, or a bool
// parameter) analyzes cleanly.
func TestIfConditionBoolOK(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\npub fn g(b: bool): int {\n  if b {\n    return 1\n  }\n  return 0\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestIfExhaustiveReturns checks that an if/else where both branches return
// counts as a return, so a function whose body is exactly such an if does not
// trip missing_return.
func TestIfExhaustiveReturns(t *testing.T) {
	_, diags := analyze("pub fn sign(n: int): int {\n  if n > 0 {\n    return 1\n  } else {\n    return -1\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestIfElseChainReturns checks that an else-if chain ending in an else, every
// branch of which returns, also counts as a return.
func TestIfElseChainReturns(t *testing.T) {
	_, diags := analyze("pub fn sign(n: int): int {\n  if n > 0 {\n    return 1\n  } else if n < 0 {\n    return -1\n  } else {\n    return 0\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestIfNoElseMissingReturn checks that an if with no else does not guarantee a
// return: a function whose body is only such a guard trips missing_return.
func TestIfNoElseMissingReturn(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeMissingReturn) {
		t.Fatalf("want missing_return for an if with no else, got %v", codes(diags))
	}
}

// TestIfElseMissingBranchReturn checks that an if/else where one branch does not
// return does not count as a return: the function still needs a trailing return.
func TestIfElseMissingBranchReturn(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  } else {\n    log(n)\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeMissingReturn) {
		t.Fatalf("want missing_return when one branch falls through, got %v", codes(diags))
	}
}

// TestIfEvalGuardDispatch checks that a guard if folds at compile time: the
// condition selects whether the then body runs (returning early) or execution
// falls through to the trailing return.
func TestIfEvalGuardDispatch(t *testing.T) {
	src := "fn warn(n: int): int {\n  if n > 100 {\n    return 0\n  }\n  return n\n}\nconst A = warn(200)\nconst B = warn(5)\n"
	if got := evalOf(t, src, "A").Int.Int64(); got != 0 {
		t.Errorf("A = %d, want 0 (guard taken)", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 5 {
		t.Errorf("B = %d, want 5 (guard not taken, falls through)", got)
	}
}

// TestIfEvalElseChainDispatch checks that an else-if chain folds by running the
// first branch whose condition holds — head, middle, and else all exercised.
func TestIfEvalElseChainDispatch(t *testing.T) {
	src := "fn sign(n: int): int {\n  if n > 0 {\n    return 1\n  } else if n < 0 {\n    return -1\n  } else {\n    return 0\n  }\n}\nconst P = sign(7)\nconst N = sign(-3)\nconst Z = sign(0)\n"
	for _, tc := range []struct {
		name string
		want int64
	}{{"P", 1}, {"N", -1}, {"Z", 0}} {
		if got := evalOf(t, src, tc.name).Int.Int64(); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestIfEvalNested checks that a nested if (in an else body) folds, so the
// branches compose at compile time.
func TestIfEvalNested(t *testing.T) {
	src := "fn classify(n: int): string {\n  if n > 0 {\n    return \"p\"\n  } else {\n    if n < 0 {\n      return \"n\"\n    }\n    return \"z\"\n  }\n}\nconst Z = classify(0)\nconst NN = classify(-1)\n"
	if got := evalOf(t, src, "Z").Str; got != "z" {
		t.Errorf("Z = %q, want z (inner guard not taken)", got)
	}
	if got := evalOf(t, src, "NN").Str; got != "n" {
		t.Errorf("NN = %q, want n (inner guard taken)", got)
	}
}

// TestIfInMethodBody checks that an if works in a method body the same way it
// does in a function body: a non-bool condition is still reported, and a bool
// one analyzes cleanly.
func TestIfInMethodBody(t *testing.T) {
	clean := "pub type Counter = { n: int }\nimpl {\n  pub fn describe(): string {\n    if self.n > 0 {\n      return \"positive\"\n    }\n    return \"other\"\n  }\n}\n"
	if _, diags := analyze(clean); len(diags) != 0 {
		t.Fatalf("clean method if: unexpected diagnostics: %v", codes(diags))
	}
	bad := "pub type Counter = { n: int }\nimpl {\n  pub fn describe(): string {\n    if self.n {\n      return \"positive\"\n    }\n    return \"other\"\n  }\n}\n"
	if _, diags := analyze(bad); !hasCodeSwitch(diags, CodeConditionNotBool) {
		t.Fatalf("non-bool method if: want condition_not_bool, got %v", codes(diags))
	}
}
