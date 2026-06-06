package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// leafText concatenates the source text of every leaf token in root, in order.
// For a lossless tree this must reproduce the original source exactly.
func leafText(buf source.Buffer, root cst.Green) string {
	var b strings.Builder
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		if _, ok := t.Token(); ok {
			b.WriteString(t.Text(buf))
			return
		}
		for _, c := range t.Children() {
			walk(c)
		}
	}
	walk(cst.Root(root))
	return b.String()
}

// assertLossless checks that the tree round-trips to the source byte for byte.
func assertLossless(t *testing.T, src string) {
	t.Helper()
	d := NewDocument([]byte(src))
	if got := leafText(d.Buffer(), d.Root()); got != src {
		t.Fatalf("tree is not lossless\n src:  %q\n leaves: %q", src, got)
	}
	if got := d.Root().Width(); got != len(src) {
		t.Fatalf("root width = %d, want %d", got, len(src))
	}
}

func TestParseLossless(t *testing.T) {
	cases := []string{
		"",
		"\n\n  \t\n",
		"const x = 1",
		"const MaxLevel: long = 100\n",
		"pub const X = Y\n",
		"const A = 1\nconst B = 2\n",
		"/// doc\nconst X = 1\n",
		"/* block */ const X = 1 // trailing\n",
		"const = \nconst X\n= = =\npub pub const Z = 9",
		"   \t  ",
		"@#$ const X = 1",
		"const x = 1+2*3\n",
		"const y = !a && b || -c\n",
		"const z = 1 <= 2 == true\n",
		"const w = - - 1\n",
		"const e = 1 +\n", // missing right operand stays lossless
		// Type declarations.
		"pub type Coin = sbyte\n",
		"type Pair = A | B\n",
		"type Opt<T> = T | null\n",
		"type Num<T: sbyte | short> = T\n",
		"type Rec = {\n  a: sbyte\n  b: Level\n}\n",
		"type Lvl = sbyte impl {\n  pub increment(): self {\n    return self + 1\n  }\n}\n",
		// Associated constants in an impl block (const items, mixed with methods).
		"type Lvl = sbyte impl {\n  pub const Max = 100\n  const Min = 0\n}\n",
		"type Lvl = sbyte impl {\n  const Max = 100\n  pub inc(): self {\n    return self\n  }\n}\n",
		"type Bits = int impl {\n  pub const Width: int = 32\n}\n",
		"type I8 = builtin impl {\n  pub const Max = builtin\n}\n",
		"type Bad = sbyte impl {\n  const = 1\n  const X\n}\n", // malformed impl consts stay lossless
		"type Mapper<T, R> = fn(src: T): R\n",
		"const x = 1\ntype T = sbyte\npub const y = 2\n", // const/type interleaved
		"type Bad =\ntype Worse <\n",                     // malformed type decls stay lossless
		// Where clauses (refinement predicates).
		"type Port = int where self >= 1 && self <= 65535\n",
		"type Pct = sbyte where self >= 0 impl {\n  inc(): self {\n    return self\n  }\n}\n",
		"type Bad = sbyte where\n",      // missing predicate stays lossless
		"type Bad = sbyte where impl\n", // a keyword starts no expression (must not consume it)
		// Function literals: annotations are optional in every position.
		"const f = fn(x: nint): nint { return x }\n",
		"const f = fn(x) { return x }\n",
		"const f = fn(x: nint, y) { return x }\n",
		"const f = fn() {}\n",
		"const f = fn(x",       // truncated literal stays lossless
		"const f = fn(x,",      // truncated after a comma (must not panic)
		"const f = fn(x,)",     // trailing comma is not part of the grammar
		"type F = fn(x: nint,", // truncated func type param list
		// Arrow bodies: a single expression after "->".
		"const f = fn(x) -> x * 2\n",
		"const f = fn(x: nint): nint -> x\n",
		"const f = fn() -> 1\n",
		"const f = fn(x) ->",                // missing arrow body stays lossless
		"const f = fn(x) -> { return 1 }\n", // block after arrow is an error, stays lossless
		"const f = fn(x) => x\n",            // a fat arrow is no body starter
		"type L = sbyte impl {\nm(x,",       // truncated method param list
		// Assert declarations.
		"assert Max > Min\n",
		"/// doc\nassert Max > Min\n",
		"const X = 1\nassert X == 1\ntype T = sbyte\n", // interleaved with other decls
		"assert\n",        // missing expression stays lossless
		"assert 1 >\n",    // missing right operand stays lossless
		"assert assert\n", // a keyword starts no expression (must not consume it)
		// Parenthesized groupings.
		"const x = (1 + 2) * 3\n",
		"const x = !(a && b)\n",
		"const x = ((1))\n",
		"const x = (1\n",   // unclosed grouping stays lossless
		"const x = ()\n",   // empty grouping stays lossless
		"const x = (1))\n", // stray closer becomes an error run
	}
	for _, src := range cases {
		assertLossless(t, src)
	}
}

// declKinds returns the kinds of the File's direct children, with token leaves
// rendered as their token kind in angle brackets.
func declKinds(root *cst.Node) []string {
	var out []string
	for _, c := range root.Children() {
		switch g := c.(type) {
		case *cst.Node:
			out = append(out, g.Kind().String())
		case *cst.Token:
			out = append(out, "<"+g.Kind().String()+">")
		}
	}
	return out
}

func TestParseFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\npub const Y: nint = 2\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "ConstDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// subNodeKinds returns the kinds of a node's direct child nodes, skipping leaf
// tokens (and therefore trivia).
func subNodeKinds(n *cst.Node) []cst.Kind {
	var out []cst.Kind
	for _, c := range n.Children() {
		if sn, ok := c.(*cst.Node); ok {
			out = append(out, sn.Kind())
		}
	}
	return out
}

// kindStrings renders kinds for joining in comparisons.
func kindStrings(kinds []cst.Kind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.String()
	}
	return out
}
