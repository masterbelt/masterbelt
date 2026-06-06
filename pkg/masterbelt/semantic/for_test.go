package semantic

import (
	"testing"
)

// TestForOfListSum checks that a for-of loop over a list folds at compile time:
// the loop accumulates each value into a let, and the function collapses to the
// sum.
func TestForOfListSum(t *testing.T) {
	src := "pub fn sum(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\nconst S = sum([1, 2, 3, 4])\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 10 {
		t.Errorf("S = %d, want 10", got)
	}
}

// TestForInMapKeys checks that a for-in loop over a map binds each entry key.
func TestForInMapKeys(t *testing.T) {
	src := "pub fn j(m: map<string, int>): string {\n  let out = \"\"\n  for k in m {\n    out = out + k\n  }\n  return out\n}\nconst J = j([\"a\": 1, \"b\": 2, \"c\": 3])\n"
	if got := evalOf(t, src, "J").Str; got != "abc" {
		t.Errorf("J = %q, want \"abc\"", got)
	}
}

// TestForOfMapValues checks that a for-of loop over a map binds the value (not
// the key), so it sums the map's values.
func TestForOfMapValues(t *testing.T) {
	src := "pub fn vs(m: map<string, int>): int {\n  let total = 0\n  for v of m {\n    total = total + v\n  }\n  return total\n}\nconst V = vs([\"a\": 1, \"b\": 2, \"c\": 3])\n"
	if got := evalOf(t, src, "V").Int.Int64(); got != 6 {
		t.Errorf("V = %d, want 6", got)
	}
}

// TestForInListIndex checks that a for-in loop over a list binds the element
// index, so summing the indices of a three-element list is 0 + 1 + 2.
func TestForInListIndex(t *testing.T) {
	src := "pub fn idx(xs: list<int>): int {\n  let total = 0\n  for i in xs {\n    total = total + i\n  }\n  return total\n}\nconst I = idx([10, 20, 30])\n"
	if got := evalOf(t, src, "I").Int.Int64(); got != 3 {
		t.Errorf("I = %d, want 3", got)
	}
}

// TestForNested checks that a nested for folds: the inner loop runs in full for
// each element of the outer, so the count is the product of the two lengths.
func TestForNested(t *testing.T) {
	src := "pub fn pc(xs: list<int>, ys: list<int>): int {\n  let n = 0\n  for x of xs {\n    for y of ys {\n      n = n + 1\n    }\n  }\n  return n\n}\nconst P = pc([1, 2], [1, 2, 3])\n"
	if got := evalOf(t, src, "P").Int.Int64(); got != 6 {
		t.Errorf("P = %d, want 6", got)
	}
}

// TestForBodyWithIf checks that a for body nesting an if folds — only the even
// elements are accumulated.
func TestForBodyWithIf(t *testing.T) {
	src := "pub fn se(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    if x % 2 == 0 {\n      total = total + x\n    }\n  }\n  return total\n}\nconst E = se([1, 2, 3, 4, 5, 6])\n"
	if got := evalOf(t, src, "E").Int.Int64(); got != 12 {
		t.Errorf("E = %d, want 12", got)
	}
}

// TestForEmptyCollection checks that a for over an empty collection skips the
// body entirely, so the accumulator keeps its initial value.
func TestForEmptyCollection(t *testing.T) {
	src := "pub fn sum(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\nconst S = sum([])\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 0 {
		t.Errorf("S = %d, want 0", got)
	}
}

// TestForEqualsFold checks the plan's headline invariant: a for loop and the
// equivalent fold yield the same result.
func TestForEqualsFold(t *testing.T) {
	src := "pub fn byFor(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\n" +
		"pub fn byFold(xs: list<int>): int {\n  return xs.fold(0, fn(acc: int, i: int, v: int): int -> acc + v)\n}\n" +
		"const F = byFor([1, 2, 3, 4, 5])\nconst D = byFold([1, 2, 3, 4, 5])\n"
	f := evalOf(t, src, "F").Int.Int64()
	d := evalOf(t, src, "D").Int.Int64()
	if f != d || f != 15 {
		t.Errorf("for = %d, fold = %d, want both 15", f, d)
	}
}

// TestForEarlyReturn checks that a return inside a for body ends the loop and the
// function with that value — and, because the body is not guaranteed to run, the
// trailing return after the loop is not dead (no missing_return).
func TestForEarlyReturn(t *testing.T) {
	src := "pub fn firstOver(xs: list<int>): int {\n  for x of xs {\n    if x > 2 {\n      return x\n    }\n  }\n  return 0\n}\nconst R = firstOver([1, 2, 3, 4])\n"
	if got := evalOf(t, src, "R").Int.Int64(); got != 3 {
		t.Errorf("R = %d, want 3", got)
	}
}

// TestForNotIterable checks that a for over a non-foldable value (an int) is
// reported as not_iterable, naming the offending type.
func TestForNotIterable(t *testing.T) {
	src := "pub fn f(n: int): int {\n  let total = 0\n  for x of n {\n    total = total + 1\n  }\n  return total\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeNotIterable) {
		t.Fatalf("want not_iterable, got %v", codes(diags))
	}
}

// TestForLoopVarImmutable checks that reassigning the loop variable is reported
// as loop_var_immutable — the loop variable is an immutable per-iteration binding.
func TestForLoopVarImmutable(t *testing.T) {
	src := "pub fn f(xs: list<int>): int {\n  for x of xs {\n    x = 1\n  }\n  return 0\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeLoopVarImmutable) {
		t.Fatalf("want loop_var_immutable, got %v", codes(diags))
	}
}

// TestForLoopVarImmutableNested checks that a reassignment buried in an if inside
// the loop body is still caught.
func TestForLoopVarImmutableNested(t *testing.T) {
	src := "pub fn f(xs: list<int>): int {\n  for x of xs {\n    if true {\n      x = 1\n    }\n  }\n  return 0\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeLoopVarImmutable) {
		t.Fatalf("want loop_var_immutable, got %v", codes(diags))
	}
}

// TestForLoopVarTyped checks that the loop variable resolves at its element type
// inside the body: an of-loop over list<int> binds x as int, so an int method on
// x type-checks and the function folds.
func TestForLoopVarTyped(t *testing.T) {
	src := "pub fn f(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    total = total + x * 2\n  }\n  return total\n}\nconst T = f([1, 2, 3])\n"
	if got := evalOf(t, src, "T").Int.Int64(); got != 12 {
		t.Errorf("T = %d, want 12", got)
	}
}

// TestForOKNoDiagnostics checks that a well-formed for produces no diagnostics —
// in particular, a body that reads (but does not write) the loop variable is fine.
func TestForOKNoDiagnostics(t *testing.T) {
	src := "pub fn f(xs: list<int>): int {\n  let total = 0\n  for x of xs {\n    total = total + x\n  }\n  return total\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// forInterfaceSrc declares a foldable interface used by the abstract-foldable for
// tests below. It mirrors the prelude's foldable so a for over an interface-typed
// value, a bounded type parameter, or a user type that opts in is exercised
// without depending on the prelude collection types.
const forInterfaceSrc = "" +
	"pub interface foldable<K, V> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: K, value: V): A): A\n" +
	"}\n"

// TestForOverInterfaceParam checks that a for over a value typed as the foldable
// interface in requirement position (c: foldable<int, int>) is iterable: the
// loop variable types at the interface's V for of (and K for in), and the
// function type-checks with no not_iterable. The same value's c.fold(...) call is
// accepted, so for must be too (plan §3.1).
func TestForOverInterfaceParam(t *testing.T) {
	src := forInterfaceSrc +
		"pub fn total(c: foldable<int, int>): int {\n" +
		"  let sum = 0\n" +
		"  for x of c {\n" +
		"    sum = sum + x\n" +
		"  }\n" +
		"  return sum\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestForOverInterfaceParamIn checks the in-loop over an interface-typed value
// binds the interface's K.
func TestForOverInterfaceParamIn(t *testing.T) {
	src := forInterfaceSrc +
		"pub fn keys(c: foldable<string, int>): string {\n" +
		"  let out = \"\"\n" +
		"  for k in c {\n" +
		"    out = out + k\n" +
		"  }\n" +
		"  return out\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestForOverBoundedTypeParam checks that a for over a bounded generic type
// parameter (T: foldable<int, int>) is iterable — the loop variable types through
// the bound's interface, exactly as a method call on the parameter resolves.
func TestForOverBoundedTypeParam(t *testing.T) {
	src := forInterfaceSrc +
		"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
		"  let sum = 0\n" +
		"  for x of c {\n" +
		"    sum = sum + x\n" +
		"  }\n" +
		"  return sum\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestForOverUserFoldableConcrete checks that a for over a concrete user type that
// opts into foldable (Bag = list<int> impl foldable<int, int>) is iterable, types
// the loop variable at the impl's element type, and folds: the underlying list
// value drives the iteration, so the accumulation collapses to a constant.
func TestForOverUserFoldableConcrete(t *testing.T) {
	src := "pub type Bag = list<int> impl foldable<int, int> {\n" +
		"  pub extern fn fold(init: A, step: fn(acc: A, key: int, value: int): A): A\n" +
		"}\n" +
		"pub fn sum(b: Bag): int {\n" +
		"  let total = 0\n" +
		"  for x of b {\n" +
		"    total = total + x\n" +
		"  }\n" +
		"  return total\n" +
		"}\n" +
		"const S = sum(Bag([1, 2, 3, 4]))\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 10 {
		t.Errorf("S = %d, want 10", got)
	}
}
