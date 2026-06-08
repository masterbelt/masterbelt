package abstract

import "testing"

// TestTrailingCommaDroppedInAST pins that a trailing comma is purely syntactic:
// the abstract tree of a list with one is byte-for-byte identical to the same
// list without it. The CST keeps the comma (it is lossless); the AST — which the
// later layers consume — does not see it, so resolution, typing, and evaluation
// are unaffected.
func TestTrailingCommaDroppedInAST(t *testing.T) {
	pairs := [][2]string{
		{"const a = [1, 2, 3,]", "const a = [1, 2, 3]"},
		{`const m = ["a": 1, "b": 2,]`, `const m = ["a": 1, "b": 2]`},
		{"const r = P{ x: 1, y: 2, }", "const r = P{ x: 1, y: 2 }"},
		{"pub type T = { x: nint, y: nint, }", "pub type T = { x: nint, y: nint }"},
		{"const c = f(1, 2,)", "const c = f(1, 2)"},
		{"pub fn g(a: nint, b: nint,): nint { return a }", "pub fn g(a: nint, b: nint): nint { return a }"},
		{"pub type M = map<nint, nint,>", "pub type M = map<nint, nint>"},
		{`use { foo, bar, } from "x"`, `use { foo, bar } from "x"`},
		{"const lam = fn(a, b,) -> a", "const lam = fn(a, b) -> a"},
	}
	for _, p := range pairs {
		withComma, without := astText(t, p[0]), astText(t, p[1])
		if withComma != without {
			t.Errorf("AST differs with vs without a trailing comma\n  %q\n  %q\n--- with ---\n%s\n--- without ---\n%s",
				p[0], p[1], withComma, without)
		}
	}
}

func astText(t *testing.T, src string) string {
	t.Helper()
	text, err := NewDocument([]byte(src)).File().MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}
