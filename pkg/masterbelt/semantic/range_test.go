package semantic

import (
	"testing"
	"time"
)

// TestRangeForOfSum checks the headline example: a for-of loop over a range sums
// its elements (range(0, 10) is 0..10), folding to 55 at compile time.
func TestRangeForOfSum(t *testing.T) {
	src := "pub fn total(): nint {\n  let t = 0\n  for i of range(0, 10) {\n    t = t + i\n  }\n  return t\n}\nconst S = total()\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 55 {
		t.Errorf("S = %d, want 55", got)
	}
}

// TestRangeForInPosition checks that a for-in loop over a range binds the 0-based
// position (the key, like a list's index), not the element: the positions of
// range(5, 10) are 0..5, summing to 15.
func TestRangeForInPosition(t *testing.T) {
	src := "pub fn ps(): nint {\n  let t = 0\n  for i in range(5, 10) {\n    t = t + i\n  }\n  return t\n}\nconst P = ps()\n"
	if got := evalOf(t, src, "P").Int.Int64(); got != 15 {
		t.Errorf("P = %d, want 15", got)
	}
}

// TestRangeEmpty checks that an end below the start is the empty range: the
// for body never runs, so the accumulator keeps its initial value.
func TestRangeEmpty(t *testing.T) {
	src := "pub fn e(): nint {\n  let t = 100\n  for i of range(10, 9) {\n    t = t + i\n  }\n  return t\n}\nconst E = e()\n"
	if got := evalOf(t, src, "E").Int.Int64(); got != 100 {
		t.Errorf("E = %d, want 100", got)
	}
}

// TestRangeFold checks the native fold: it threads an accumulator over the
// elements, the step seeing the 0-based position as its key. Summing the values
// of range(0, 4) is 0 + 1 + 2 + 3 + 4 = 10; summing the keys is the same here.
func TestRangeFold(t *testing.T) {
	src := "const F = range(0, 4).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n" +
		"const K = range(0, 4).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + k)\n"
	if got := evalOf(t, src, "F").Int.Int64(); got != 10 {
		t.Errorf("F = %d, want 10", got)
	}
	if got := evalOf(t, src, "K").Int.Int64(); got != 10 {
		t.Errorf("K = %d, want 10", got)
	}
}

// TestRangeProvidedMethods checks the foldable provided methods range carries for
// free: count, any, and all fold to their scalars, and the list-returning ones
// (map, filter, keys, values) fold to lists whose len and fold are verifiable.
func TestRangeProvidedMethods(t *testing.T) {
	src := "const C = range(0, 10).count()\n" +
		"const EC = range(5, 4).count()\n" +
		"const A = range(0, 10).any(fn(v: nint): bool -> v > 8)\n" +
		"const L = range(0, 10).all(fn(v: nint): bool -> v >= 0)\n" +
		"const ML = range(0, 5).map(fn(v: nint): nint -> v * 2).len()\n" +
		"const FL = range(0, 10).filter(fn(v: nint): bool -> v % 2 == 0).len()\n" +
		"const VS = range(3, 7).values().fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n" +
		"const KS = range(3, 7).keys().fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n"
	if got := evalOf(t, src, "C").Int.Int64(); got != 11 {
		t.Errorf("count = %d, want 11", got)
	}
	if got := evalOf(t, src, "EC").Int.Int64(); got != 0 {
		t.Errorf("empty count = %d, want 0", got)
	}
	if got := evalOf(t, src, "A").Bool; !got {
		t.Errorf("any(v > 8) = false, want true")
	}
	if got := evalOf(t, src, "L").Bool; !got {
		t.Errorf("all(v >= 0) = false, want true")
	}
	if got := evalOf(t, src, "ML").Int.Int64(); got != 6 {
		t.Errorf("map().len() = %d, want 6", got)
	}
	if got := evalOf(t, src, "FL").Int.Int64(); got != 6 {
		t.Errorf("filter().len() = %d, want 6", got)
	}
	if got := evalOf(t, src, "VS").Int.Int64(); got != 25 { // 3 + 4 + 5 + 6 + 7
		t.Errorf("values sum = %d, want 25", got)
	}
	if got := evalOf(t, src, "KS").Int.Int64(); got != 10 { // 0 + 1 + 2 + 3 + 4
		t.Errorf("keys sum = %d, want 10", got)
	}
}

// TestRangeArity checks the constructor's argument count: range takes two or
// three arguments. A one- or four-argument call is an arity_mismatch; the two-
// and three-argument forms are both valid (the third is the step).
func TestRangeArity(t *testing.T) {
	for _, src := range []string{
		"const R = range(0)\n",
		"const R = range(0, 10, 2, 3)\n",
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeArityMismatch) {
			t.Errorf("src %q: want arity_mismatch, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{
		"const R = range(0, 10)\n",
		"const R = range(0, 10, 2)\n",
	} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("src %q: want no diagnostic, got %v", src, codes(diags))
		}
	}
}

// TestRangeStepConstructor checks the three-argument constructor: a positive step
// walks up while v <= end (range(0, 10, 2) is 0,2,4,6,8,10), a negative step
// walks down while v >= end (range(10, 0, -2) is 10,8,6,4,2,0). Both fold their
// element count and their sum.
func TestRangeStepConstructor(t *testing.T) {
	src := "const UpCount = range(0, 10, 2).count()\n" +
		"const UpSum = range(0, 10, 2).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n" +
		"const DownCount = range(10, 0, -2).count()\n" +
		"const DownSum = range(10, 0, -2).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n"
	if got := evalOf(t, src, "UpCount").Int.Int64(); got != 6 {
		t.Errorf("range(0, 10, 2).count() = %d, want 6", got)
	}
	if got := evalOf(t, src, "UpSum").Int.Int64(); got != 30 { // 0+2+4+6+8+10
		t.Errorf("range(0, 10, 2) sum = %d, want 30", got)
	}
	if got := evalOf(t, src, "DownCount").Int.Int64(); got != 6 {
		t.Errorf("range(10, 0, -2).count() = %d, want 6", got)
	}
	if got := evalOf(t, src, "DownSum").Int.Int64(); got != 30 { // 10+8+6+4+2+0
		t.Errorf("range(10, 0, -2) sum = %d, want 30", got)
	}
}

// TestRangeStepPartialStride checks a step that does not divide the span: the
// last element lands inside the bound, never past it. range(0, 9, 2) is
// 0,2,4,6,8 (5 elements; 9 is skipped because 10 > 9), and range(0, 10, 3) is
// 0,3,6,9 (4 elements; 12 > 10). The count is (end-start)/step + 1, flooring.
func TestRangeStepPartialStride(t *testing.T) {
	src := "const A = range(0, 9, 2).count()\n" +
		"const B = range(0, 10, 3).count()\n" +
		"const C = range(9, 0, -2).count()\n" // 9,7,5,3,1 -> 5 (0 skipped, -1 < 0)
	if got := evalOf(t, src, "A").Int.Int64(); got != 5 {
		t.Errorf("range(0, 9, 2).count() = %d, want 5", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 4 {
		t.Errorf("range(0, 10, 3).count() = %d, want 4", got)
	}
	if got := evalOf(t, src, "C").Int.Int64(); got != 5 {
		t.Errorf("range(9, 0, -2).count() = %d, want 5", got)
	}
}

// TestRangeStepEmpty checks an end past the start against the step's sign: the
// range is empty. range(0, 10, -1) walks down from 0 but 0 < 10, so it visits
// nothing; range(10, 0, 1) walks up from 10 but 10 > 0, likewise empty.
func TestRangeStepEmpty(t *testing.T) {
	src := "const A = range(0, 10, -1).count()\n" +
		"const B = range(10, 0, 1).count()\n"
	if got := evalOf(t, src, "A").Int.Int64(); got != 0 {
		t.Errorf("range(0, 10, -1).count() = %d, want 0", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 0 {
		t.Errorf("range(10, 0, 1).count() = %d, want 0", got)
	}
}

// TestRangeStepZero checks the zero-step diagnostic: a range whose step folds to
// a constant zero has no sequence (it neither advances nor terminates), reported
// as range_step_zero. A non-constant step is left to the runtime — no diagnostic.
func TestRangeStepZero(t *testing.T) {
	if _, diags := analyze("const R = range(0, 10, 0)\n"); !hasCode(diags, CodeRangeStepZero) {
		t.Errorf("range(0, 10, 0): want range_step_zero, got %v", codes(diags))
	}
	// A zero-step range does not fold to a value: the const stays unevaluated.
	mod, _ := analyze("const R = range(0, 10, 0)\n")
	for _, c := range mod.Consts {
		if c.Name == "R" && c.Eval != nil {
			t.Errorf("range(0, 10, 0) folded to %v, want unevaluated", c.Eval)
		}
	}
	// A non-constant step is not diagnosed (left to the runtime).
	nonConst := "pub fn r(s: nint): range {\n  return range(0, 10, s)\n}\n"
	if _, diags := analyze(nonConst); hasCode(diags, CodeRangeStepZero) {
		t.Errorf("range(0, 10, s) with a non-constant step: want no diagnostic, got %v", codes(diags))
	}
}

// TestRangeStepAtBound checks the iteration cap honours the step: a stepped range
// is counted from its bounds in O(1), so a range whose stepped element count is
// at the cap still folds and one past it does not — the limit is the visit count,
// not the bound magnitudes. With step 2 the span is twice the cap.
func TestRangeStepAtBound(t *testing.T) {
	const iterCap = 1 << 20                                 // 1048576
	src := "const AtCap = range(0, 2097150, 2).count()\n" + // 2097150/2 + 1 = 1048576 == cap
		"const OverCap = range(0, 2097152, 2).count()\n" // 2097152/2 + 1 = 1048577 > cap
	mod, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	got := map[string]*int64{}
	for _, c := range mod.Consts {
		if c.Eval != nil && c.Eval.Int != nil {
			v := c.Eval.Int.Int64()
			got[c.Name] = &v
		}
	}
	if got["AtCap"] == nil || *got["AtCap"] != iterCap {
		t.Errorf("AtCap = %v, want %d (a stepped range at the cap still folds)", got["AtCap"], iterCap)
	}
	if got["OverCap"] != nil {
		t.Errorf("OverCap folded to %d, want unevaluated (past the cap must not fold)", *got["OverCap"])
	}
}

// TestRangeNonIntArg checks that a non-integer bound is a type_mismatch: range
// constructs from two ints.
func TestRangeNonIntArg(t *testing.T) {
	src := "const R = range(\"a\", 10)\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch, got %v", codes(diags))
	}
}

// TestRangeHugeIsSafe is the safety invariant: a range wider than the evaluator's
// iteration bound must not be folded — neither hanging the folder nor exhausting
// memory. A range is constructed lazily from its bounds (so range(0, 1e9) is a
// small value); only a fold or a for over it would materialize the sequence, and
// that walk is capped. The const over the wide fold simply does not evaluate
// (Eval == nil), and analysis returns promptly. The bound itself is exercised by
// a range one element wider than the cap, which is the smallest range that must
// not fold — proving the limit is the count, not the bound magnitudes.
func TestRangeHugeIsSafe(t *testing.T) {
	// A billion-element fold and for: a naive eager walk would hang or OOM.
	src := "pub fn big(): nint {\n  let t = 0\n  for i of range(0, 1000000000) {\n    t = t + i\n  }\n  return t\n}\n" +
		"const Forl = big()\n" +
		"const Foldl = range(0, 1000000000).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n" +
		"const Counted = range(0, 1000000000).count()\n"

	done := make(chan struct{})
	var m struct {
		forl, foldl, counted *bool
	}
	go func() {
		defer close(done)
		mod, diags := analyze(src)
		// A wide range that does not fold is not a diagnostic: a const may be
		// unevaluated. The only requirement is that none of these consts folded.
		_ = diags
		seen := map[string]bool{}
		for _, c := range mod.Consts {
			if c.Eval != nil {
				seen[c.Name] = true
			}
		}
		f1, f2, f3 := seen["Forl"], seen["Foldl"], seen["Counted"]
		m.forl, m.foldl, m.counted = &f1, &f2, &f3
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("analyzing a billion-element range did not finish in 20s — the iteration bound is not protecting the folder")
	}
	if m.forl == nil || *m.forl {
		t.Error("a billion-element for folded — the iteration bound did not stop it")
	}
	if m.foldl == nil || *m.foldl {
		t.Error("a billion-element fold folded — the iteration bound did not stop it")
	}
	if m.counted == nil || *m.counted {
		t.Error("count over a billion-element range folded — the iteration bound did not stop it")
	}
}

// TestRangeAtBound checks the iteration limit precisely: a range whose element
// count is at the cap still folds, and one a single element wider does not — so
// the limit is exactly the element count, applied before the walk. The fold here
// is count(), whose only cost is the visit count, so the at-bound case stays fast.
func TestRangeAtBound(t *testing.T) {
	const iterCap = 1 << 20                              // mirrors eval.maxRangeIterations
	src := "const AtCap = range(0, 1048575).count()\n" + // exactly the cap
		"const OverCap = range(0, 1048576).count()\n" // one wider

	mod, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	got := map[string]*int64{}
	for _, c := range mod.Consts {
		if c.Eval != nil && c.Eval.Int != nil {
			v := c.Eval.Int.Int64()
			got[c.Name] = &v
		} else {
			got[c.Name] = nil
		}
	}
	if got["AtCap"] == nil || *got["AtCap"] != iterCap {
		t.Errorf("AtCap = %v, want %d (a range at the cap still folds)", got["AtCap"], iterCap)
	}
	if got["OverCap"] != nil {
		t.Errorf("OverCap folded to %d, want unevaluated (a range past the cap must not fold)", *got["OverCap"])
	}
}
