package semantic

import (
	"testing"
	"time"
)

// TestRangeForOfSum checks the headline example: a for-of loop over a range sums
// its elements (range(0, 10) is 0..9), folding to 45 at compile time.
func TestRangeForOfSum(t *testing.T) {
	src := "pub fn total(): int {\n  let t = 0\n  for i of range(0, 10) {\n    t = t + i\n  }\n  return t\n}\nconst S = total()\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 45 {
		t.Errorf("S = %d, want 45", got)
	}
}

// TestRangeForInPosition checks that a for-in loop over a range binds the 0-based
// position (the key, like a list's index), not the element: the positions of
// range(5, 10) are 0..4, summing to 10.
func TestRangeForInPosition(t *testing.T) {
	src := "pub fn ps(): int {\n  let t = 0\n  for i in range(5, 10) {\n    t = t + i\n  }\n  return t\n}\nconst P = ps()\n"
	if got := evalOf(t, src, "P").Int.Int64(); got != 10 {
		t.Errorf("P = %d, want 10", got)
	}
}

// TestRangeEmpty checks that an end at or below the start is the empty range: the
// for body never runs, so the accumulator keeps its initial value.
func TestRangeEmpty(t *testing.T) {
	src := "pub fn e(): int {\n  let t = 100\n  for i of range(10, 10) {\n    t = t + i\n  }\n  return t\n}\nconst E = e()\n"
	if got := evalOf(t, src, "E").Int.Int64(); got != 100 {
		t.Errorf("E = %d, want 100", got)
	}
}

// TestRangeFold checks the native fold: it threads an accumulator over the
// elements, the step seeing the 0-based position as its key. Summing the values
// of range(0, 4) is 0 + 1 + 2 + 3 = 6; summing the keys is the same here.
func TestRangeFold(t *testing.T) {
	src := "const F = range(0, 4).fold(0, fn(acc: int, k: int, v: int): int -> acc + v)\n" +
		"const K = range(0, 4).fold(0, fn(acc: int, k: int, v: int): int -> acc + k)\n"
	if got := evalOf(t, src, "F").Int.Int64(); got != 6 {
		t.Errorf("F = %d, want 6", got)
	}
	if got := evalOf(t, src, "K").Int.Int64(); got != 6 {
		t.Errorf("K = %d, want 6", got)
	}
}

// TestRangeProvidedMethods checks the foldable provided methods range carries for
// free: count, any, and all fold to their scalars, and the list-returning ones
// (map, filter, keys, values) fold to lists whose len and fold are verifiable.
func TestRangeProvidedMethods(t *testing.T) {
	src := "const C = range(0, 10).count()\n" +
		"const EC = range(5, 5).count()\n" +
		"const A = range(0, 10).any(fn(v: int): bool -> v > 8)\n" +
		"const L = range(0, 10).all(fn(v: int): bool -> v >= 0)\n" +
		"const ML = range(0, 5).map(fn(v: int): int -> v * 2).len()\n" +
		"const FL = range(0, 10).filter(fn(v: int): bool -> v % 2 == 0).len()\n" +
		"const VS = range(3, 7).values().fold(0, fn(acc: int, k: int, v: int): int -> acc + v)\n" +
		"const KS = range(3, 7).keys().fold(0, fn(acc: int, k: int, v: int): int -> acc + v)\n"
	if got := evalOf(t, src, "C").Int.Int64(); got != 10 {
		t.Errorf("count = %d, want 10", got)
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
	if got := evalOf(t, src, "ML").Int.Int64(); got != 5 {
		t.Errorf("map().len() = %d, want 5", got)
	}
	if got := evalOf(t, src, "FL").Int.Int64(); got != 5 {
		t.Errorf("filter().len() = %d, want 5", got)
	}
	if got := evalOf(t, src, "VS").Int.Int64(); got != 18 { // 3 + 4 + 5 + 6
		t.Errorf("values sum = %d, want 18", got)
	}
	if got := evalOf(t, src, "KS").Int.Int64(); got != 6 { // 0 + 1 + 2 + 3
		t.Errorf("keys sum = %d, want 6", got)
	}
}

// TestRangeArity checks the constructor's argument count: range takes exactly two
// arguments today. A one- or three-argument call is an arity_mismatch — the
// three-argument form is rejected by the same rule, leaving the door open for a
// future range(start, end, step) to add an arm rather than change this check.
func TestRangeArity(t *testing.T) {
	for _, src := range []string{
		"const R = range(0)\n",
		"const R = range(0, 10, 2)\n",
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeArityMismatch) {
			t.Errorf("src %q: want arity_mismatch, got %v", src, codes(diags))
		}
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
	src := "pub fn big(): int {\n  let t = 0\n  for i of range(0, 1000000000) {\n    t = t + i\n  }\n  return t\n}\n" +
		"const Forl = big()\n" +
		"const Foldl = range(0, 1000000000).fold(0, fn(acc: int, k: int, v: int): int -> acc + v)\n" +
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
	src := "const AtCap = range(0, 1048576).count()\n" + // exactly the cap
		"const OverCap = range(0, 1048577).count()\n" // one wider

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
