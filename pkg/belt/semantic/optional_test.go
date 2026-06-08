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
// binds T from the argument, so the call type-checks. The fold of a match whose
// arm type is the function's type parameter (T n -> ...) dispatches through the
// call's settled substitution (T = nint), which the evaluator threads into the
// body: the constant folds to the bound member, with no diagnostic. (This was
// once an open evaluator gap, loud as unfolded_const; closing it flipped the pin
// to the folded value.)
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
	if len(diags) != 0 {
		t.Fatalf("codes = %v, want none (the generic arm now dispatches through the substitution)", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Name != "A" {
			continue
		}
		if c.Eval == nil {
			t.Fatal("A did not fold; want the bound member 5")
		}
		if got := c.Eval.String(); got != "5" {
			t.Fatalf("A folded to %q, want 5", got)
		}
	}
}

// TestGenericUnionAccumulatorFolds pins the std:math reduction shape end to end:
// a generic function whose nested fold lambda annotates its accumulator with a
// union over the type parameter (T | error) and dispatches it with a match. It
// exercises three fixes at once — the lambda annotation resolving T inside the
// union (not optional<invalid>), the generic match arm folding through the
// call's substitution, and the result never carrying the type variable as a
// union tag — and it must fold each element exactly once (the error seed flows
// in only on the first step), so a list of three sums to 6, not 7.
func TestGenericUnionAccumulatorFolds(t *testing.T) {
	src := `pub fn sum<T: numeric>(xs: list<T>): T | error {
  let total: T | error = error("empty")
  return xs.fold(total, fn(acc: T | error, i: nint, v: T): T | error {
    match acc {
      T a -> return a + v
      error e -> return v
    }
  })
}
const S = sum([1, 2, 3])
const E: list<nint> = []
const Empty = sum(E)
`
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("codes = %v, want none", codes(diags))
	}
	want := map[string]string{"S": "6", "Empty": `error("empty")`}
	for _, c := range m.Consts {
		exp, ok := want[c.Name]
		if !ok {
			continue
		}
		if c.Eval == nil {
			t.Fatalf("%s did not fold; want %s", c.Name, exp)
		}
		if got := c.Eval.String(); got != exp {
			t.Errorf("%s folded to %q, want %s", c.Name, got, exp)
		}
	}
}
