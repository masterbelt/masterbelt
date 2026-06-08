package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
)

// TestRangeLiteralEqualsConstructor pins the unified rule: each of the four
// literal forms folds to exactly the range(...) it desugars to. a..b is the
// closed interval iterated from a toward b; a...b is the same iterated set with
// the larger end (the max) excluded. The equality folds by start/end/step, so
// the literal and its constructor compare equal at compile time.
func TestRangeLiteralEqualsConstructor(t *testing.T) {
	cases := []struct {
		name string
		expr string // a range-literal expression
		ctor string // the range(...) it must equal
	}{
		{"closed up", "0..9", "range(0, 9)"},
		{"half-open up", "0...9", "range(0, 8)"},
		{"closed down", "9..0", "range(9, 0, -1)"},
		{"half-open down", "9...0", "range(8, 0, -1)"},
		{"single closed", "5..5", "range(5, 5)"},
		{"single half-open empty", "5...5", "range(5, 4)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "const R = (" + tc.expr + ") == " + tc.ctor + "\n"
			v := evalOf(t, src, "R")
			if v == nil || !v.Bool {
				t.Errorf("(%s) == %s did not fold true (got %v)", tc.expr, tc.ctor, v)
			}
		})
	}
}

// TestRangeLiteralForOf checks for-of over each literal form sums the elements
// the equivalent range(...) would: ascending, descending, and half-open.
func TestRangeLiteralForOf(t *testing.T) {
	cases := []struct {
		name string
		lit  string
		want int64
	}{
		{"closed up", "0..10", 55},      // 0+1+...+10
		{"closed down", "5..1", 15},     // 5+4+3+2+1
		{"half-open up", "0...5", 10},   // 0+1+2+3+4 (5 excluded)
		{"half-open down", "5...0", 10}, // 4+3+2+1+0 (5, the max, excluded)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "pub fn s(): nint {\n  let t = 0\n  for i of " + tc.lit + " {\n    t = t + i\n  }\n  return t\n}\nconst S = s()\n"
			if got := evalOf(t, src, "S").Int.Int64(); got != tc.want {
				t.Errorf("for of %s summed %d, want %d", tc.lit, got, tc.want)
			}
		})
	}
}

// TestRangeLiteralArithmeticBound pins the precedence: an arithmetic bound binds
// tighter than the range, so 0..n+1 is 0..(n+1). With n=4 the literal is 0..5,
// whose six elements (0..5) sum to 15.
func TestRangeLiteralArithmeticBound(t *testing.T) {
	src := "pub fn s(n: nint): nint {\n  let t = 0\n  for i of 0..n + 1 {\n    t = t + i\n  }\n  return t\n}\nconst S = s(4)\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 15 {
		t.Errorf("for of 0..n + 1 with n=4 summed %d, want 15 (0..5)", got)
	}
}

// TestRangeLiteralProvidedMethods checks a range literal reaches the foldable
// provided methods exactly as a range(...) value does: count and fold.
func TestRangeLiteralProvidedMethods(t *testing.T) {
	src := "const C = (0..9).count()\n" +
		"const HC = (0...9).count()\n" +
		"const F = (0..4).fold(0, fn(acc: nint, k: nint, v: nint): nint -> acc + v)\n"
	if got := evalOf(t, src, "C").Int.Int64(); got != 10 {
		t.Errorf("(0..9).count() = %d, want 10", got)
	}
	if got := evalOf(t, src, "HC").Int.Int64(); got != 9 {
		t.Errorf("(0...9).count() = %d, want 9", got)
	}
	if got := evalOf(t, src, "F").Int.Int64(); got != 10 { // 0+1+2+3+4
		t.Errorf("(0..4).fold sum = %d, want 10", got)
	}
}

// TestRangeLiteralNonIntBound checks a non-integer bound is a type_mismatch, the
// same rule the range(...) constructor's arguments take.
func TestRangeLiteralNonIntBound(t *testing.T) {
	src := "const R = \"a\"..9\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch, got %v", codes(diags))
	}
}

// TestRangeLiteralChainIsParseError checks the non-associativity: a chain
// a..b..c is rejected at the parser, so it never reaches a valid IR.
func TestRangeLiteralChainIsParseError(t *testing.T) {
	doc := abstract.NewDocument([]byte("const R = 1..2..3\n"))
	if len(doc.Diagnostics()) == 0 {
		t.Errorf("a..b..c chain parsed clean, want a parse diagnostic")
	}
}
