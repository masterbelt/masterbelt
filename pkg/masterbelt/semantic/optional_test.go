package semantic

import "testing"

// TestOptionalAssign pins that the prelude optional<T> alias accepts a member
// value and the null literal — the named-union assignability the alias rides
// on — and rejects a non-member.
func TestOptionalAssign(t *testing.T) {
	if got := evalOf(t, "const A: optional<nint> = 5\n", "A").String(); got != "5" {
		t.Fatalf("A folded to %q, want 5", got)
	}
	if got := evalOf(t, "const B: optional<nint> = null\n", "B").String(); got != "null" {
		t.Fatalf("B folded to %q, want null", got)
	}
	_, diags := analyze("const C: optional<nint> = \"s\"\n")
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic assigning a string to optional<nint>")
	}
}

// TestOptionalMatch pins that match narrows the alias exactly as the bare
// union: both arms fold, and the alias is exhaustive with int + null.
func TestOptionalMatch(t *testing.T) {
	src := `pub fn orZero(v: optional<nint>): nint {
  match v {
    nint n -> return n
    _ -> return 0
  }
}
const A = orZero(5)
const B = orZero(null)
assert orZero(7) == 7
assert orZero(null) == 0
`
	if got := evalOf(t, src, "A").String(); got != "5" {
		t.Fatalf("A folded to %q, want 5", got)
	}
	if got := evalOf(t, src, "B").String(); got != "0" {
		t.Fatalf("B folded to %q, want 0", got)
	}
}

// TestOptionalNonExhaustiveMatch pins that exhaustiveness checking sees
// through the alias: a match missing the null arm is reported.
func TestOptionalNonExhaustiveMatch(t *testing.T) {
	src := `pub fn bad(v: optional<nint>): nint {
  match v {
    nint n -> return n
  }
  return 0
}
`
	_, diags := analyze(src)
	if !hasCode(diags, CodeNonExhaustiveMatch) {
		t.Fatalf("expected non_exhaustive_match, got %v", codes(diags))
	}
}

// TestOptionalGenericSolve pins that a generic function solves T through the
// alias parameter: optional<T> expands to T | null and Match's union recursion
// binds T from the argument, so the call type-checks. The fold of a match
// whose arm type is the function's type parameter (T n -> ...) is a capability
// the type-blind evaluator does not have — it resolves an arm type through a
// universe lookup, which a generic parameter is not in, so the dispatch is
// undecided. Under the fold-totality rule that open gap is loud rather than
// silent: the constant errs with unfolded_const (reason: evaluator gap) until
// the evaluator learns generic arm dispatch — at which point this pin flips to
// the folded value below.
func TestOptionalGenericSolve(t *testing.T) {
	src := `pub fn orFallback<T>(v: optional<T>, fb: T): T {
  match v {
    T n -> return n
    _ -> return fb
  }
}
const A = orFallback(5, 0)
`
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnfoldedConst {
		t.Fatalf("codes = %v, want [unfolded_const] (the generic-arm dispatch gap is loud, not silent)", got)
	}
	for _, c := range m.Consts {
		if c.Name == "A" && c.Eval != nil {
			// A fold is a bonus, not the contract: if the evaluator ever learns to
			// dispatch a generic match arm, the value must be the bound member.
			if got := c.Eval.String(); got != "5" {
				t.Fatalf("A folded to %q, want 5", got)
			}
		}
	}
}
