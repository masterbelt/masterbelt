package concrete

import "testing"

// TestParseTrailingComma pins that every comma-separated bracketed list accepts
// an optional trailing comma: it parses without a diagnostic and the tree stays
// lossless (the trailing comma is a leaf the round-trip reproduces). The call
// argument list, parameter list, generic argument list, and use list are the
// ones this relaxes; the collection, map, record, generic-parameter, and enum
// lists already accepted one and are kept here as a guard.
func TestParseTrailingComma(t *testing.T) {
	cases := []string{
		// Already accepted before this change — kept as a regression guard.
		"const a = [1, 2, 3,]",
		`const m = ["a": 1, "b": 2,]`,
		"const r = P{ x: 1, y: 2, }",
		"pub type T = { x: nint, y: nint, }",
		"pub enum E { A, B, }",
		"pub fn k<T, U,>(a: T): T { return a }",

		// Newly accepted.
		"const c = f(1, 2,)",
		"pub fn g(a: nint, b: nint,): nint { return a }",
		"pub type M = map<nint, nint,>",
		`use { foo, bar, } from "x"`,
		"const lam = fn(a, b,) -> a",

		// Multi-line forms — the shape the formatter emits.
		"const x = combine(\n  1,\n  2,\n  3,\n)",
		"pub fn p(\n  a: nint,\n  b: nint,\n): nint {\n  return a\n}",
	}
	for _, src := range cases {
		d := NewDocument([]byte(src))
		if diags := d.Diagnostics(); len(diags) != 0 {
			t.Errorf("parse %q: unexpected parse diagnostics %v", src, diags)
		}
		assertLossless(t, src)
	}
}

// TestParseTrailingCommaTruncationStillErrors pins that the relaxation does not
// swallow a genuinely truncated list: a comma with neither another element nor a
// closing bracket after it is still a parse error, not a silently accepted
// trailing comma.
func TestParseTrailingCommaTruncationStillErrors(t *testing.T) {
	cases := []string{
		"pub fn g(a: nint,", // parameter list runs off the end
		`use { foo,`,        // use list runs off the end
	}
	for _, src := range cases {
		d := NewDocument([]byte(src))
		if len(d.Diagnostics()) == 0 {
			t.Errorf("parse %q: expected a diagnostic for the truncated list", src)
		}
		assertLossless(t, src)
	}
}
