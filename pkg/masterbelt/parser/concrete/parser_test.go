package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
		"const MaxLevel: int64 = 100\n",
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
		"pub type Coin = int8\n",
		"type Pair = A | B\n",
		"type Opt<T> = T | null\n",
		"type Num<T: int8 | int16> = T\n",
		"type Rec = {\n  a: int8\n  b: Level\n}\n",
		"type Lvl = int8 impl {\n  pub increment(): self {\n    return self + 1\n  }\n}\n",
		// Associated constants in an impl block (const items, mixed with methods).
		"type Lvl = int8 impl {\n  pub const Max = 100\n  const Min = 0\n}\n",
		"type Lvl = int8 impl {\n  const Max = 100\n  pub inc(): self {\n    return self\n  }\n}\n",
		"type Bits = int32 impl {\n  pub const Width: int32 = 32\n}\n",
		"type I8 = builtin impl {\n  pub const Max = builtin\n}\n",
		"type Bad = int8 impl {\n  const = 1\n  const X\n}\n", // malformed impl consts stay lossless
		"type Mapper<T, R> = fn(src: T): R\n",
		"const x = 1\ntype T = int8\npub const y = 2\n", // const/type interleaved
		"type Bad =\ntype Worse <\n",                    // malformed type decls stay lossless
		// Where clauses (refinement predicates).
		"type Port = int32 where self >= 1 && self <= 65535\n",
		"type Pct = int8 where self >= 0 impl {\n  inc(): self {\n    return self\n  }\n}\n",
		"type Bad = int8 where\n",      // missing predicate stays lossless
		"type Bad = int8 where impl\n", // a keyword starts no expression (must not consume it)
		// Function literals: annotations are optional in every position.
		"const f = fn(x: int): int { return x }\n",
		"const f = fn(x) { return x }\n",
		"const f = fn(x: int, y) { return x }\n",
		"const f = fn() {}\n",
		"const f = fn(x",      // truncated literal stays lossless
		"const f = fn(x,",     // truncated after a comma (must not panic)
		"const f = fn(x,)",    // trailing comma is not part of the grammar
		"type F = fn(x: int,", // truncated func type param list
		// Arrow bodies: a single expression after "->".
		"const f = fn(x) -> x * 2\n",
		"const f = fn(x: int): int -> x\n",
		"const f = fn() -> 1\n",
		"const f = fn(x) ->",                // missing arrow body stays lossless
		"const f = fn(x) -> { return 1 }\n", // block after arrow is an error, stays lossless
		"const f = fn(x) => x\n",            // a fat arrow is no body starter
		"type L = int8 impl {\nm(x,",        // truncated method param list
		// Assert declarations.
		"assert Max > Min\n",
		"/// doc\nassert Max > Min\n",
		"const X = 1\nassert X == 1\ntype T = int8\n", // interleaved with other decls
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
	root, diags := Parse([]byte("const X = 1\npub const Y: int = 2\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "ConstDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

func TestParseConstDeclChildren(t *testing.T) {
	root, _ := Parse([]byte("const Max: int64 = 100"))
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.ConstDecl {
		t.Fatalf("first child kind = %s, want ConstDecl", decl.Kind())
	}
	var nodeKinds []cst.Kind
	for _, c := range decl.Children() {
		if n, ok := c.(*cst.Node); ok {
			nodeKinds = append(nodeKinds, n.Kind())
		}
	}
	if len(nodeKinds) != 2 || nodeKinds[0] != cst.TypeClause || nodeKinds[1] != cst.Initializer {
		t.Fatalf("decl sub-nodes = %v, want [TypeClause Initializer]", nodeKinds)
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

// TestParseTypeDeclFileShape checks that type declarations are recognised at the
// file level and that the const/type choice is made by looking past pub.
func TestParseTypeDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\npub type Coin = int8\ntype Pair = A | B\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "TypeDecl", "TypeDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

func TestParseTypeDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"nominal", "type Coin = int8\n", []cst.Kind{cst.TypeName}},
		{"union", "type Pair = A | B\n", []cst.Kind{cst.UnionType}},
		{"generic union", "pub type Opt<T> = T | null\n", []cst.Kind{cst.GenericParams, cst.UnionType}},
		{"constrained generic", "type Num<T: int8 | int16> = T\n", []cst.Kind{cst.GenericParams, cst.TypeName}},
		{"record", "type Rec = {\n  a: int8\n}\n", []cst.Kind{cst.RecordType}},
		{"func type", "type M<T, R> = fn(src: T): R\n", []cst.Kind{cst.GenericParams, cst.FuncType}},
		{"impl", "type Lvl = int8 impl {\n  pub inc(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeName, cst.ImplBlock}},
		{"null name", "pub type null = builtin\n", []cst.Kind{cst.BuiltinType}}, // null may be declared
		{"where", "type Port = int32 where self <= 65535\n", []cst.Kind{cst.TypeName, cst.WhereClause}},
		{"where impl", "type Pct = int8 where self >= 0 impl {\n  inc(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeName, cst.WhereClause, cst.ImplBlock}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.TypeDecl {
				t.Fatalf("first child kind = %s, want TypeDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplConst checks that an impl block's associated-constant items are
// recognised as ConstDecl nodes (the same node a top-level constant uses),
// alongside its methods, and that the const/method choice looks past pub.
func TestParseImplConst(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind // the impl block's direct child node kinds
	}{
		{"pub const and bare const", "type L = int8 impl {\n  pub const Max = 100\n  const Min = 0\n}\n",
			[]cst.Kind{cst.ConstDecl, cst.ConstDecl}},
		{"const then method", "type L = int8 impl {\n  const Max = 100\n  pub inc(): self {\n    return self\n  }\n}\n",
			[]cst.Kind{cst.ConstDecl, cst.MethodDecl}},
		{"typed const", "type B = int32 impl {\n  pub const Width: int32 = 32\n}\n",
			[]cst.Kind{cst.ConstDecl}},
		{"builtin const", "type I8 = builtin impl {\n  pub const Max = builtin\n}\n",
			[]cst.Kind{cst.ConstDecl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var impl *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.ImplBlock {
					impl = n
				}
			}
			if impl == nil {
				t.Fatalf("no impl block found in %q", tc.src)
			}
			got := subNodeKinds(impl)
			if len(got) != len(tc.want) {
				t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplConstChildren checks the sub-node shape of an associated
// constant: a typed one carries a TypeClause then an Initializer; an untyped
// one only an Initializer; and a "= builtin" one an Initializer wrapping a
// BuiltinType.
func TestParseImplConstChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"untyped", "type L = int8 impl {\n  const Max = 100\n}\n", []cst.Kind{cst.Initializer}},
		{"typed", "type B = int32 impl {\n  const Width: int32 = 32\n}\n", []cst.Kind{cst.TypeClause, cst.Initializer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var c *cst.Node
			for _, child := range decl.Children() {
				if n, ok := child.(*cst.Node); ok && n.Kind() == cst.ImplBlock {
					for _, ic := range n.Children() {
						if cn, ok := ic.(*cst.Node); ok && cn.Kind() == cst.ConstDecl {
							c = cn
						}
					}
				}
			}
			if c == nil {
				t.Fatalf("no impl ConstDecl found in %q", tc.src)
			}
			got := subNodeKinds(c)
			if len(got) != len(tc.want) {
				t.Fatalf("const sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("const sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseInterfaceDeclFileShape checks that an interface declaration is
// recognised at the file level, the choice made by looking past pub.
func TestParseInterfaceDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("pub interface foldable<T> {\n  fold(init: T): T\n}\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"InterfaceDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseInterfaceDeclChildren checks the sub-node shape of an interface: its
// optional generic parameters and its members (required and provided alike land
// as InterfaceMember nodes).
func TestParseInterfaceDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"required only", "interface eq {\n  eql(other: self): bool\n}\n",
			[]cst.Kind{cst.InterfaceMember}},
		{"generic with provided", "pub interface foldable<K, V> {\n  fold<A>(init: A): A\n  pub count(): int {\n    return 0\n  }\n}\n",
			[]cst.Kind{cst.GenericParams, cst.InterfaceMember, cst.InterfaceMember}},
		{"empty", "interface marker {\n}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.InterfaceDecl {
				t.Fatalf("first child kind = %s, want InterfaceDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseInterfaceMemberChildren checks that a required member carries no
// Block (only its ParamList and result type) while a provided member carries a
// Block, and that an explicit member type parameter lands as GenericParams.
func TestParseInterfaceMemberChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"required", "interface i {\n  m(x: int): int\n}\n",
			[]cst.Kind{cst.ParamList, cst.TypeName}},
		{"provided", "interface i {\n  m(x: int): int {\n    return x\n  }\n}\n",
			[]cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"generic required", "interface i {\n  fold<A>(init: A): A\n}\n",
			[]cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var member *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.InterfaceMember {
					member = n
				}
			}
			if member == nil {
				t.Fatalf("no interface member found in %q", tc.src)
			}
			got := subNodeKinds(member)
			if len(got) != len(tc.want) {
				t.Fatalf("member sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("member sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseImplInterfaceTag checks that the optional interface name after impl
// lands as a TypeName child of the impl block, before its brace, while a bare
// impl carries no such tag.
func TestParseImplInterfaceTag(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind // the impl block's direct child node kinds
	}{
		{"tagged", "type Bag<T> = list<T> impl foldable<int, T> {\n  fold<A>(init: A): A {\n    return init\n  }\n}\n",
			[]cst.Kind{cst.TypeName, cst.MethodDecl}},
		{"bare", "type L = int8 impl {\n  inc(): self {\n    return self\n  }\n}\n",
			[]cst.Kind{cst.MethodDecl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			var impl *cst.Node
			for _, c := range decl.Children() {
				if n, ok := c.(*cst.Node); ok && n.Kind() == cst.ImplBlock {
					impl = n
				}
			}
			if impl == nil {
				t.Fatalf("no impl block found in %q", tc.src)
			}
			got := subNodeKinds(impl)
			if len(got) != len(tc.want) {
				t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("impl child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseWhereClauseDiagnostics checks local recovery for malformed
// where-clauses.
func TestParseWhereClauseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing predicate", "type Bad = int8 where\n", CodeExpectedExpression},
		{"keyword predicate", "type Bad = int8 where impl {}\n", CodeExpectedExpression},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// exprSexpr renders an expression subtree as a parenthesized infix form, with
// trivia skipped, so precedence and associativity are easy to assert. Operators
// render as their text; literals and names render as their text.
func exprSexpr(buf source.Buffer, t cst.Tree) string {
	if _, ok := t.Token(); ok {
		return strings.TrimSpace(t.Text(buf))
	}
	if k, _ := t.Kind(); k == cst.Literal || k == cst.NameRef {
		return strings.TrimSpace(t.Text(buf))
	}
	var parts []string
	for _, c := range t.Children() {
		if tk, ok := c.TokenKind(); ok && isTrivia(tk) {
			continue
		}
		parts = append(parts, exprSexpr(buf, c))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// initExpr parses src and returns the first declaration's initializer
// expression subtree.
func initExpr(t *testing.T, src string) (source.Buffer, cst.Tree) {
	t.Helper()
	d := NewDocument([]byte(src))
	for _, decl := range cst.Root(d.Root()).Children() {
		if k, ok := decl.Kind(); !ok || k != cst.ConstDecl {
			continue
		}
		for _, ch := range decl.Children() {
			if k, ok := ch.Kind(); !ok || k != cst.Initializer {
				continue
			}
			for _, ic := range ch.Children() {
				if k, ok := ic.Kind(); ok && k != cst.Error {
					return d.Buffer(), ic
				}
			}
		}
	}
	t.Fatalf("no initializer expression in %q", src)
	panic("unreachable")
}

func TestParseExpressionShape(t *testing.T) {
	cases := []struct{ src, want string }{
		{"const x = 1 + 2\n", "(1 + 2)"},
		{"const x = 1 + 2 * 3\n", "(1 + (2 * 3))"}, // * binds tighter than +
		{"const x = 1 * 2 + 3\n", "((1 * 2) + 3)"},
		{"const x = 1 - 2 - 3\n", "((1 - 2) - 3)"}, // left-associative
		{"const x = 1 < 2 && 3 > 4\n", "((1 < 2) && (3 > 4))"},
		{"const x = a || b && c\n", "(a || (b && c))"}, // && binds tighter than ||
		{"const x = -1 + 2\n", "((- 1) + 2)"},          // unary binds tightest
		{"const x = !a && b\n", "((! a) && b)"},
		{"const x = - - 1\n", "(- (- 1))"},
		{"const x = true\n", "true"},
		{"const x = false || true\n", "(false || true)"},
		{"const x = \"hi\"\n", `"hi"`},                 // a string literal is an operand
		{"const x = \"a\" == \"b\"\n", `("a" == "b")`}, // strings compose like any operand
	}
	for _, tc := range cases {
		buf, e := initExpr(t, tc.src)
		if got := exprSexpr(buf, e); got != tc.want {
			t.Errorf("%q: shape = %s, want %s", tc.src, got, tc.want)
		}
	}
}

// TestParseTernary checks the conditional value expression: its low precedence
// (binds looser than every binary operator), its right-associative nesting, and
// that the ":" in a map entry is not mistaken for a ternary branch separator.
func TestParseTernary(t *testing.T) {
	cases := []struct{ src, want string }{
		// The condition is a full binary expression; the ternary wraps it.
		{"const x = a ? b : c\n", "(a ? b : c)"},
		{"const x = a > b ? a : b\n", "((a > b) ? a : b)"}, // ?: binds looser than >
		{"const x = a + b ? c : d\n", "((a + b) ? c : d)"}, // looser than + too
		// Right-associative: the else-branch is itself a ternary.
		{"const x = a ? b : c ? d : e\n", "(a ? b : (c ? d : e))"},
		// The branches are full expressions, operators and all.
		{"const x = a ? b + 1 : c * 2\n", "(a ? (b + 1) : (c * 2))"},
	}
	for _, tc := range cases {
		buf, e := initExpr(t, tc.src)
		if got := exprSexpr(buf, e); got != tc.want {
			t.Errorf("%q: shape = %s, want %s", tc.src, got, tc.want)
		}
		if _, diags := Parse([]byte(tc.src)); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", tc.src, diags)
		}
	}

	// The ":" inside a list still separates a map key from its value, even when a
	// branch of the entry is a ternary: a ternary consumes its own ":", so the
	// remaining top-level ":" is the map separator.
	t.Run("map entry beside a ternary value", func(t *testing.T) {
		_, e := initExpr(t, "const x = [\"k\": a ? b : c]\n")
		if k, ok := e.Kind(); !ok || k != cst.CollectionLit {
			t.Fatalf("initializer kind = %v, want CollectionLit", k)
		}
		got := collectionChildKinds(e)
		if len(got) != 1 || got[0] != cst.MapEntry {
			t.Fatalf("child nodes = %v, want [MapEntry]", got)
		}
	})

	// A missing else-branch is reported but the node still round-trips losslessly.
	t.Run("missing else branch", func(t *testing.T) {
		_, diags := Parse([]byte("const x = a ? b\n"))
		if len(diags) == 0 {
			t.Fatal("expected a diagnostic for the missing : branch")
		}
		assertLossless(t, "const x = a ? b\n")
	})
}

// collectionChildKinds returns the kinds of a CollectionLit's direct child
// nodes (skipping the bracket/comma/colon leaves and trivia).
func collectionChildKinds(t cst.Tree) []cst.Kind {
	var out []cst.Kind
	for _, c := range t.Children() {
		if k, ok := c.Kind(); ok {
			out = append(out, k)
		}
	}
	return out
}

func TestParseCollectionLiteral(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"list", "const x = [1, 2, 3]\n", []cst.Kind{cst.Literal, cst.Literal, cst.Literal}},
		{"map", "const x = [\"a\": 1, \"b\": 2]\n", []cst.Kind{cst.MapEntry, cst.MapEntry}},
		{"empty", "const x = []\n", nil},
		{"nested", "const x = [[1], [2]]\n", []cst.Kind{cst.CollectionLit, cst.CollectionLit}},
		{"trailing comma", "const x = [1, 2,]\n", []cst.Kind{cst.Literal, cst.Literal}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			_, e := initExpr(t, tc.src)
			if k, ok := e.Kind(); !ok || k != cst.CollectionLit {
				t.Fatalf("initializer kind = %v, want CollectionLit", k)
			}
			got := collectionChildKinds(e)
			if len(got) != len(tc.want) {
				t.Fatalf("child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseIndexExpr checks the postfix index access: it forms a left-leaning
// IndexExpr after any operand, chains with the other postfixes (a call, a member
// access, another index), and binds tighter than every binary operator. The
// leading "[" of a collection literal stays an operand, so a subscript and a
// literal never collide.
func TestParseIndexExpr(t *testing.T) {
	cases := []struct{ src, want string }{
		// A bare list/map subscript.
		{"const x = xs[0]\n", "(xs [ 0 ])"},
		{"const x = m[\"k\"]\n", `(m [ "k" ])`},
		// The index is a full expression.
		{"const x = xs[i + 1]\n", "(xs [ (i + 1) ])"},
		// Chains, left to right: a call then an index, and an index then an index.
		{"const x = f()[0]\n", "((f ( )) [ 0 ])"},
		{"const x = xs[0][1]\n", "((xs [ 0 ]) [ 1 ])"},
		// A member access composes with an index either way.
		{"const x = a.b[0]\n", "((a . b) [ 0 ])"},
		{"const x = xs[0].b\n", "((xs [ 0 ]) . b)"},
		// Tighter than a binary operator: the subscript binds before "+".
		{"const x = a + xs[i]\n", "(a + (xs [ i ]))"},
		// A collection literal can itself be indexed: the first "[" is the literal
		// (an operand), the second "[" is the subscript.
		{"const x = [1, 2][0]\n", "(([ 1 , 2 ]) [ 0 ])"},
	}
	for _, tc := range cases {
		buf, e := initExpr(t, tc.src)
		if got := exprSexpr(buf, e); got != tc.want {
			t.Errorf("%q: shape = %s, want %s", tc.src, got, tc.want)
		}
		if _, diags := Parse([]byte(tc.src)); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", tc.src, diags)
		}
		assertLossless(t, tc.src)
	}

	// The outermost subscript is an IndexExpr node, and a leading collection
	// literal stays a CollectionLit — the two are distinct kinds.
	t.Run("kinds", func(t *testing.T) {
		_, e := initExpr(t, "const x = xs[0]\n")
		if k, ok := e.Kind(); !ok || k != cst.IndexExpr {
			t.Fatalf("initializer kind = %v, want IndexExpr", k)
		}
		_, lit := initExpr(t, "const x = [1, 2]\n")
		if k, ok := lit.Kind(); !ok || k != cst.CollectionLit {
			t.Fatalf("literal kind = %v, want CollectionLit", k)
		}
	})

	// A missing index expression and a missing "]" are reported, and the source
	// still round-trips losslessly.
	t.Run("diagnostics", func(t *testing.T) {
		for _, src := range []string{"const x = xs[]\n", "const x = xs[0\n"} {
			if _, diags := Parse([]byte(src)); len(diags) == 0 {
				t.Errorf("%q: expected a diagnostic", src)
			}
			assertLossless(t, src)
		}
	})
}

// TestParseFuncDecl checks the top-level function declaration: both body
// forms, the pub modifier, and the file-level dispatch on fn followed by a
// name.
func TestParseFuncDecl(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"block body", "fn area(w: int, h: int): int {\n  return w * h\n}\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"arrow body", "fn double(x: int): int -> x * 2\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.BinaryExpr}},
		{"pub", "pub fn zero(): int -> 0\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Literal}},
		{"record result", "pub fn origin(): Point -> Point{ x: 0 }\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.RecordLit}},
		{"unbounded type param", "fn id<T>(x: T): int -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"bounded type param", "fn total<T: foldable<int, int>>(c: T): int -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"several type params", "fn pair<T, U>(a: T, b: U): int -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
		{"parameterized bound", "fn first<T: foldable<U>, U>(c: T): int -> 0\n", []cst.Kind{cst.GenericParams, cst.ParamList, cst.TypeName, cst.Literal}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			assertLossless(t, tc.src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.FuncDecl {
				t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseFnThreeUses pins the three uses of fn apart: a function type (type
// position), a function literal (value position, no name), and a function
// declaration (top level, a name follows) — all in one file, parse-clean.
func TestParseFnThreeUses(t *testing.T) {
	src := "type F = fn(x: int): int\nconst g = fn(x) -> x\nfn h(x: int): int -> x\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	assertLossless(t, src)
	got := declKinds(root)
	want := []string{"TypeDecl", "ConstDecl", "FuncDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseFuncDeclRecovery checks the malformed declaration forms: a pub fn
// missing its name still parses as a (reported) declaration, a bare nameless
// fn is a stray expression, and an error run stops before a following
// function declaration.
func TestParseFuncDeclRecovery(t *testing.T) {
	t.Run("pub fn without a name is a reported FuncDecl", func(t *testing.T) {
		src := "pub fn(x: int): int -> x\n"
		root, diags := Parse([]byte(src))
		if len(diags) == 0 {
			t.Fatal("want a diagnostic for the missing name")
		}
		assertLossless(t, src)
		decl := root.Children()[0].(*cst.Node)
		if decl.Kind() != cst.FuncDecl {
			t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
		}
	})
	t.Run("bare nameless fn is an error run", func(t *testing.T) {
		src := "fn(x: int): int -> x\n"
		root, diags := Parse([]byte(src))
		if len(diags) == 0 {
			t.Fatal("want a diagnostic for the stray literal")
		}
		assertLossless(t, src)
		decl := root.Children()[0].(*cst.Node)
		if decl.Kind() != cst.Error {
			t.Fatalf("first child kind = %s, want Error", decl.Kind())
		}
	})
	t.Run("an error run stops before a fn declaration", func(t *testing.T) {
		src := "1 + 2\nfn h(): int -> 0\n"
		root, diags := Parse([]byte(src))
		if len(diags) == 0 {
			t.Fatal("want a diagnostic for the stray expression")
		}
		assertLossless(t, src)
		got := declKinds(root)
		want := []string{"Error", "FuncDecl", "<Newline>", "<EOF>"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("file children = %v, want %v", got, want)
		}
	})
	t.Run("missing body is reported", func(t *testing.T) {
		_, diags := Parse([]byte("fn h(): int\n"))
		found := false
		for _, d := range diags {
			if d.Code == CodeExpectedFuncBody {
				found = true
			}
		}
		if !found {
			t.Fatalf("want expected_func_body, got %v", diags)
		}
		assertLossless(t, "fn h(): int\n")
	})
	t.Run("arrow block body is reported", func(t *testing.T) {
		_, diags := Parse([]byte("fn h(): int -> { return 1 }\n"))
		found := false
		for _, d := range diags {
			if d.Code == CodeArrowBlockBody {
				found = true
			}
		}
		if !found {
			t.Fatalf("want arrow_block_body, got %v", diags)
		}
	})
}

// TestParseRecordLiteral checks both record-literal forms — typed (Point{...})
// and inferred ({...}) — across the separator styles, nesting, and the empty
// literal. The child kinds skip the brace/comma leaves and trivia, so a typed
// literal looks exactly like an inferred one here; the leading Ident leaf is
// asserted separately.
func TestParseRecordLiteral(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		typed bool
		want  []cst.Kind
	}{
		{"typed single line", "const x = Point{ x: 1, y: 2 }\n", true, []cst.Kind{cst.RecordField, cst.RecordField}},
		{"inferred", "const x: Point = { x: 1, y: 2 }\n", false, []cst.Kind{cst.RecordField, cst.RecordField}},
		{"newline separated", "const x = Point{\n  x: 1\n  y: 2\n}\n", true, []cst.Kind{cst.RecordField, cst.RecordField}},
		{"trailing comma", "const x = Point{ x: 1, y: 2, }\n", true, []cst.Kind{cst.RecordField, cst.RecordField}},
		{"nested", "const x = Item{ pos: Point{ x: 1 } }\n", true, []cst.Kind{cst.RecordField}},
		{"empty", "const x: Unit = {}\n", false, nil},
		{"empty typed", "const x = Unit{}\n", true, nil},
		{"record in list", "const x = [{ x: 1 }]\n", false, nil}, // asserted below via the literal's parent
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			assertLossless(t, tc.src)
			if tc.name == "record in list" {
				return // parse-clean and lossless is the point here
			}
			_, e := initExpr(t, tc.src)
			if k, ok := e.Kind(); !ok || k != cst.RecordLit {
				t.Fatalf("initializer kind = %v, want RecordLit", k)
			}
			gotTyped := false
			if tk, ok := e.Children()[0].TokenKind(); ok && tk == token.Ident {
				gotTyped = true
			}
			if gotTyped != tc.typed {
				t.Fatalf("typed = %v, want %v", gotTyped, tc.typed)
			}
			got := collectionChildKinds(e)
			if len(got) != len(tc.want) {
				t.Fatalf("child nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("child nodes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestParseRecordLiteralVsMap pins the bracket split: "[K: V]" stays a map
// (CollectionLit/MapEntry) and "{...}" is always a record — the two literals
// never collide.
func TestParseRecordLiteralVsMap(t *testing.T) {
	_, e := initExpr(t, "const x = [\"k\": { a: 1 }]\n")
	if k, ok := e.Kind(); !ok || k != cst.CollectionLit {
		t.Fatalf("initializer kind = %v, want CollectionLit", k)
	}
	kinds := collectionChildKinds(e)
	if len(kinds) != 1 || kinds[0] != cst.MapEntry {
		t.Fatalf("child nodes = %v, want [MapEntry]", kinds)
	}
}

// TestParseRecordTypeSeparators checks that a record type accepts comma
// separators alongside newlines, matching the literal's separator rule.
func TestParseRecordTypeSeparators(t *testing.T) {
	cases := []string{
		"type Point = { x: int, y: int }\n",
		"type Point = { x: int, y: int, }\n",
		"type Point = {\n  x: int\n  y: int\n}\n",
	}
	for _, src := range cases {
		root, diags := Parse([]byte(src))
		if len(diags) != 0 {
			t.Fatalf("%q: unexpected diagnostics: %v", src, diags)
		}
		assertLossless(t, src)
		decl := root.Children()[0].(*cst.Node)
		got := subNodeKinds(decl)
		if len(got) != 1 || got[0] != cst.RecordType {
			t.Fatalf("%q: sub-nodes = %v, want [RecordType]", src, got)
		}
	}
}

// TestParseRecordLiteralDiagnostics checks local recovery for malformed record
// literals: every case reports, stays lossless, and never swallows the
// following declaration.
func TestParseRecordLiteralDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing colon", "const x = Point{ x 1 }\nconst y = 2\n", CodeUnexpectedToken},
		{"missing value", "const x = Point{ x: }\nconst y = 2\n", CodeExpectedExpression},
		{"missing close", "const x = Point{ x: 1\n", CodeUnexpectedToken},
		{"stray comma", "const x = Point{ , x: 1 }\nconst y = 2\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseFuncLit checks the function-literal header: the parameter and result
// annotations are optional (the checker may infer them from context), and the
// shape of the node reflects exactly what was written.
func TestParseFuncLit(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind   // FuncLit's direct sub-nodes
		prm  [][]cst.Kind // each Param's sub-nodes (annotation present or not)
	}{
		{
			"fully annotated", "const f = fn(x: int): int { return x }\n",
			[]cst.Kind{cst.ParamList, cst.TypeName, cst.Block},
			[][]cst.Kind{{cst.TypeName}},
		},
		{
			"no result", "const f = fn(x: int) { return x }\n",
			[]cst.Kind{cst.ParamList, cst.Block},
			[][]cst.Kind{{cst.TypeName}},
		},
		{
			"no annotations", "const f = fn(x) { return x }\n",
			[]cst.Kind{cst.ParamList, cst.Block},
			[][]cst.Kind{nil},
		},
		{
			"partially annotated", "const f = fn(x: int, y) { return x }\n",
			[]cst.Kind{cst.ParamList, cst.Block},
			[][]cst.Kind{{cst.TypeName}, nil},
		},
		{
			"result only", "const f = fn(x): int { return x }\n",
			[]cst.Kind{cst.ParamList, cst.TypeName, cst.Block},
			[][]cst.Kind{nil},
		},
		{
			"zero params", "const f = fn() {}\n",
			[]cst.Kind{cst.ParamList, cst.Block},
			nil,
		},
		{
			"nested", "const f = fn(x) { return fn(y) { return y } }\n",
			[]cst.Kind{cst.ParamList, cst.Block},
			[][]cst.Kind{nil},
		},
		// Arrow bodies: "->" followed by a single expression instead of a block.
		{
			"arrow", "const f = fn(x) -> x * 2\n",
			[]cst.Kind{cst.ParamList, cst.BinaryExpr},
			[][]cst.Kind{nil},
		},
		{
			"arrow annotated param", "const f = fn(x: int) -> x * 3\n",
			[]cst.Kind{cst.ParamList, cst.BinaryExpr},
			[][]cst.Kind{{cst.TypeName}},
		},
		{
			"arrow two params", "const f = fn(x, y) -> x\n",
			[]cst.Kind{cst.ParamList, cst.NameRef},
			[][]cst.Kind{nil, nil},
		},
		{
			"arrow zero params", "const f = fn() -> 1\n",
			[]cst.Kind{cst.ParamList, cst.Literal},
			nil,
		},
		{
			"arrow with result", "const f = fn(x): int -> x\n",
			[]cst.Kind{cst.ParamList, cst.TypeName, cst.NameRef},
			[][]cst.Kind{nil},
		},
		{
			"arrow nested", "const f = fn(x) -> fn(y) -> y\n",
			[]cst.Kind{cst.ParamList, cst.FuncLit},
			[][]cst.Kind{nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			_, e := initExpr(t, tc.src)
			if k, ok := e.Kind(); !ok || k != cst.FuncLit {
				t.Fatalf("initializer kind = %v, want FuncLit", k)
			}
			node, _ := e.Node()
			if got := subNodeKinds(node); strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.want), ",") {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			var params []cst.Tree
			for _, c := range e.Children() {
				if k, ok := c.Kind(); ok && k == cst.ParamList {
					for _, pc := range c.Children() {
						if pk, ok := pc.Kind(); ok && pk == cst.Param {
							params = append(params, pc)
						}
					}
				}
			}
			if len(params) != len(tc.prm) {
				t.Fatalf("got %d params, want %d", len(params), len(tc.prm))
			}
			for i, p := range params {
				pn, _ := p.Node()
				got := subNodeKinds(pn)
				if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.prm[i]), ",") {
					t.Fatalf("param %d sub-nodes = %v, want %v", i, got, tc.prm[i])
				}
			}
		})
	}
}

// kindStrings renders kinds for joining in comparisons.
func kindStrings(kinds []cst.Kind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.String()
	}
	return out
}

// TestParamAnnotationStillRequired checks that relaxing the function-literal
// header did not leak into the forms whose signatures are the source of types:
// method declarations and function types still require parameter annotations,
// and a written ":" still promises a type everywhere.
func TestParamAnnotationStillRequired(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"method param", "type L = int8 impl {\n  m(x): self {\n    return self\n  }\n}\n"},
		{"func type param", "type F = fn(x): int\n"},
		{"dangling colon in func lit", "const f = fn(x:) { return x }\n"},
		{"dangling result colon in func lit", "const f = fn(x): { return x }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == CodeExpectedType {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, CodeExpectedType, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseGenericConstAnnotation checks that a constant's type annotation is a
// full type expression, so generic types like list<int> are accepted.
func TestParseGenericConstAnnotation(t *testing.T) {
	root, diags := Parse([]byte("const x: list<int> = [1]\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	var clause *cst.Node
	for _, c := range decl.Children() {
		if n, ok := c.(*cst.Node); ok && n.Kind() == cst.TypeClause {
			clause = n
		}
	}
	if clause == nil {
		t.Fatal("no TypeClause")
	}
	if got := subNodeKinds(clause); len(got) != 1 || got[0] != cst.TypeName {
		t.Fatalf("type clause sub-nodes = %v, want [TypeName]", got)
	}
}

// --- use declarations ---------------------------------------------------------

// TestParseUseDeclForms checks that every use form — namespace, selective,
// wildcard, and their pub re-export variants — parses to a single clean
// UseDecl file child.
func TestParseUseDeclForms(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"namespace", "use geo from \"geometry.belt\"\n"},
		{"selective", "use { Point, Vector } from \"shapes.belt\"\n"},
		{"wildcard", "use * from \"prelude.belt\"\n"},
		{"re-export", "pub use { Color } from \"palette.belt\"\n"},
		{"barrel", "pub use * from \"geometry.belt\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl, ok := root.Children()[0].(*cst.Node)
			if !ok || decl.Kind() != cst.UseDecl {
				t.Fatalf("first child = %v, want UseDecl", root.Children()[0])
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseUseListChildren(t *testing.T) {
	root, _ := Parse([]byte("use { Point, Vector } from \"shapes.belt\""))
	decl := root.Children()[0].(*cst.Node)
	if kinds := subNodeKinds(decl); len(kinds) != 1 || kinds[0] != cst.UseList {
		t.Fatalf("decl sub-nodes = %v, want [UseList]", kinds)
	}
}

// TestParseUseDiagnostics checks local recovery for malformed use
// declarations: each case reports its specific diagnostic and stays lossless.
func TestParseUseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing target", "use from \"a.belt\"\n", CodeExpectedIdentifier},
		{"missing from", "use geo \"a.belt\"\n", CodeExpectedFrom},
		{"missing path", "use geo from\n", CodeExpectedPath},
		{"empty list", "use {} from \"a.belt\"\n", CodeExpectedIdentifier},
		{"name after comma", "use { a, } from \"x.belt\"\n", CodeExpectedIdentifier},
		{"junk after star", "use * x from \"a.belt\"\n", CodeExpectedFrom},
		{"unclosed list", "use { a from \"x.belt\"\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseParenExpr checks a parenthesized grouping forms a ParenExpr operand
// that overrides the operator precedence around it.
func TestParseParenExpr(t *testing.T) {
	root, diags := Parse([]byte("const x = (1 + 2) * 3"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	init := decl.Children()[len(decl.Children())-1].(*cst.Node) // the Initializer
	mul := init.Children()[len(init.Children())-1].(*cst.Node)  // the BinaryExpr
	if mul.Kind() != cst.BinaryExpr {
		t.Fatalf("initializer expr = %s, want BinaryExpr", mul.Kind())
	}
	// The grouping binds tighter: the multiplication's left operand is the
	// ParenExpr, not a bare literal.
	if kinds := subNodeKinds(mul); len(kinds) != 2 || kinds[0] != cst.ParenExpr || kinds[1] != cst.Literal {
		t.Fatalf("mul sub-nodes = %v, want [ParenExpr Literal]", kinds)
	}
}

// TestParseParenExprDiagnostics checks local recovery for malformed groupings.
func TestParseParenExprDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"empty", "const x = ()\n", CodeExpectedExpression},
		{"unclosed", "const x = (1\n", CodeUnexpectedToken},
		{"unclosed nested", "const x = ((1)\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssertDeclForms checks well-formed assertions parse to an
// AssertDecl whose only sub-node is the asserted expression.
func TestParseAssertDeclForms(t *testing.T) {
	cases := []struct {
		name string
		src  string
		expr cst.Kind
	}{
		{"name", "assert Enabled\n", cst.NameRef},
		{"comparison", "assert MaxLevel > MinLevel\n", cst.BinaryExpr},
		{"logical", "assert A == 1 && !B\n", cst.BinaryExpr},
		{"documented", "/// the range is not empty\nassert Max > Min\n", cst.BinaryExpr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl, ok := root.Children()[0].(*cst.Node)
			if !ok || decl.Kind() != cst.AssertDecl {
				t.Fatalf("first child = %v, want AssertDecl", root.Children()[0])
			}
			if kinds := subNodeKinds(decl); len(kinds) != 1 || kinds[0] != tc.expr {
				t.Fatalf("decl sub-nodes = %v, want [%s]", kinds, tc.expr)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssertDeclFileShape checks assertions are recognised at the file
// level and interleave with the other declaration forms.
func TestParseAssertDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\nassert X > 0\ntype Coin = int8\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "AssertDecl", "TypeDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

// TestParseAssertDiagnostics checks local recovery for malformed assertions:
// each case reports its specific diagnostic and stays lossless.
func TestParseAssertDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing expr", "assert\n", CodeExpectedExpression},
		{"missing expr before decl", "assert\nconst X = 1\n", CodeExpectedExpression},
		{"missing rhs", "assert 1 >\n", CodeExpectedOperand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing name", "const = 1", CodeExpectedIdentifier},
		{"missing assign", "const X\n", CodeExpectedAssign},
		{"missing expr", "const X = \n", CodeExpectedExpression},
		{"missing rhs", "const X = 1 +\n", CodeExpectedOperand},
		{"missing unary operand", "const X = -\n", CodeExpectedOperand},
		{"missing type", "const X: = 1", CodeExpectedType},
		{"stray token", "= 1\n", CodeUnexpectedToken},
		{"param after comma", "const f = fn(x,) { return x }\n", CodeExpectedIdentifier},
		{"func lit without parens", "const f = fn x -> x * 2\n", CodeExpectedParamList},
		{"fat arrow is no body", "const f = fn(x) => x * 2\n", CodeExpectedFuncBody},
		{"block after arrow", "const f = fn(x) -> { return 1 }\n", CodeArrowBlockBody},
		{"missing arrow body", "const f = fn(x) ->\n", CodeExpectedExpression},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			// Even malformed input must stay lossless.
			assertLossless(t, tc.src)
		})
	}
}

// TestExpectedOperandNamesOperator checks the operand-expected diagnostic is
// specific to the operator, not the generic "expected expression".
func TestExpectedOperandNamesOperator(t *testing.T) {
	cases := []struct{ src, operator string }{
		{"const X = 1 +\n", "+"},
		{"const X = 1 &&\n", "&&"},
		{"const X = !\n", "!"},
	}
	for _, tc := range cases {
		_, diags := Parse([]byte(tc.src))
		var msg string
		for _, d := range diags {
			if d.Code == CodeExpectedOperand {
				msg = d.Message
			}
		}
		want := "expected operand after '" + tc.operator + "'"
		if msg != want {
			t.Errorf("src %q: message = %q, want %q", tc.src, msg, want)
		}
	}
}

func TestDiagnosticOffsetsResolve(t *testing.T) {
	d := NewDocument([]byte("const = 1\n"))
	diags := d.Diagnostics()
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic")
	}
	// The "expected identifier" points just after "const ".
	span := diags[0].Span(d.Buffer())
	if span.Start.Line != 1 {
		t.Fatalf("diagnostic line = %d, want 1", span.Start.Line)
	}
}

// findQualifiedTypeName returns the first TypeName node carrying a Dot child —
// the qualified form geo.Point — anywhere under g.
func findQualifiedTypeName(g cst.Green) *cst.Node {
	n, ok := g.(*cst.Node)
	if !ok {
		return nil
	}
	if n.Kind() == cst.TypeName {
		for _, c := range n.Children() {
			if tok, ok := c.(*cst.Token); ok && tok.Kind() == token.Dot {
				return n
			}
		}
	}
	for _, c := range n.Children() {
		if hit := findQualifiedTypeName(c); hit != nil {
			return hit
		}
	}
	return nil
}

// TestParseQualifiedTypeName: a namespace-qualified type name (geo.Point)
// parses into a single TypeName holding qualifier, dot, and name, in every
// type position.
func TestParseQualifiedTypeName(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"annotation", "const a: geo.Point = 1\n"},
		{"generic argument", "const a: list<geo.Point> = [1]\n"},
		{"union member", "type P = geo.Point | null\n"},
		{"record field", "type R = {\n  p: geo.Point\n}\n"},
		{"func type", "type F = fn(p: geo.Point): geo.Point\n"},
		{"applied", "const a: geo.Box<int> = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			name := findQualifiedTypeName(root)
			if name == nil {
				t.Fatal("no qualified TypeName in the tree")
			}
			var kinds []token.Kind
			for _, c := range name.Children() {
				if tok, ok := c.(*cst.Token); ok {
					kinds = append(kinds, tok.Kind())
				}
			}
			want := []token.Kind{token.Ident, token.Dot, token.Ident}
			if len(kinds) != len(want) {
				t.Fatalf("token children = %v, want %v", kinds, want)
			}
			for i := range want {
				if kinds[i] != want[i] {
					t.Fatalf("token children = %v, want %v", kinds, want)
				}
			}
		})
	}
}

// TestParseQualifiedTypeNameRecovery: a dangling qualifier (geo.) reports
// expected_identifier, and the declaration still closes over its initializer.
func TestParseQualifiedTypeNameRecovery(t *testing.T) {
	root, diags := Parse([]byte("const a: geo. = 1\n"))
	if len(diags) != 1 || diags[0].Code != CodeExpectedIdentifier {
		t.Fatalf("diagnostics = %v, want exactly expected_identifier", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.ConstDecl {
		t.Fatalf("first child = %s, want ConstDecl", decl.Kind())
	}
	got := subNodeKinds(decl)
	want := []cst.Kind{cst.TypeClause, cst.Initializer}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sub-nodes = %v, want %v (recovery must keep the initializer)", got, want)
	}
}

func TestParseEffects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"effects on fn", "pub fn io async get(url: string): string {\n  return url\n}\n", []cst.Kind{cst.ParamList, cst.TypeName, cst.Block}},
		{"extern fn", "extern fn io async fetch(url: string): string\n", []cst.Kind{cst.ParamList, cst.TypeName}},
		{"pub extern fn", "pub extern fn nondet now(): int\n", []cst.Kind{cst.ParamList, cst.TypeName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			assertLossless(t, tc.src)
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.FuncDecl {
				t.Fatalf("first child kind = %s, want FuncDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
		})
	}

	// A non-extern function still requires a body.
	if _, diags := Parse([]byte("fn io f(): int\n")); len(diags) == 0 {
		t.Errorf("fn without body: want a diagnostic")
	}
	// A method may carry effects, with or without fn.
	for _, src := range []string{
		"type C = { u: string } impl {\n  pub fn io async get(): string {\n    return self.u\n  }\n}\n",
		"type C = { u: string } impl {\n  io get(): string {\n    return self.u\n  }\n}\n",
	} {
		if _, diags := Parse([]byte(src)); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", src, diags)
		}
		assertLossless(t, src)
	}
}

func TestParseAwaitExpr(t *testing.T) {
	src := "fn io async f(u: string): string {\n  return await g(u).trim()\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	assertLossless(t, src)
	tree := cst.Sprint(source.NewFile("", []byte(src)), root)
	if !strings.Contains(tree, "AwaitExpr") {
		t.Errorf("tree = %s, want an AwaitExpr node", tree)
	}

	// A dangling await reports a missing operand.
	if _, diags := Parse([]byte("const x = await\n")); len(diags) == 0 {
		t.Errorf("dangling await: want a diagnostic")
	}
}

// enumMemberNames returns the identifier text of an enum's members, in source
// order, and the count of members that carry an initializer. It reads through
// the positioned tree so the member text resolves to its source bytes.
func enumMemberNames(buf source.Buffer, decl *cst.Node) (names []string, withValue int) {
	tree := cst.Root(decl)
	for _, c := range tree.Children() {
		if k, ok := c.Kind(); !ok || k != cst.EnumMember {
			continue
		}
		gotName := false
		for _, mc := range c.Children() {
			if tk, ok := mc.TokenKind(); ok && tk == token.Ident && !gotName {
				names = append(names, strings.TrimSpace(mc.Text(buf)))
				gotName = true
			}
			if k, ok := mc.Kind(); ok && k == cst.Initializer {
				withValue++
			}
		}
	}
	return names, withValue
}

// TestParseEnumDeclFileShape checks that enum declarations are recognised at
// the file level, the enum/const/type choice made by looking past pub.
func TestParseEnumDeclFileShape(t *testing.T) {
	root, diags := Parse([]byte("const X = 1\npub enum Rarity: uint8 {\n  A = 1\n}\nenum E {\n  B\n}\n"))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	got := declKinds(root)
	want := []string{"ConstDecl", "EnumDecl", "EnumDecl", "<Newline>", "<EOF>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("file children = %v, want %v", got, want)
	}
}

func TestParseEnumDeclChildren(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cst.Kind
	}{
		{"base type", "enum R: uint8 {\n  A = 1\n}\n", []cst.Kind{cst.TypeClause, cst.EnumMember}},
		{"no base", "enum E {\n  A\n}\n", []cst.Kind{cst.EnumMember}},
		{"comma separated", "enum E {\n  A, B, C\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember, cst.EnumMember}},
		{"newline separated", "enum E {\n  A\n  B\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember}},
		{"trailing comma", "enum E {\n  A, B,\n}\n", []cst.Kind{cst.EnumMember, cst.EnumMember}},
		{"impl", "enum E {\n  A\n} impl {\n  f(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.EnumMember, cst.ImplBlock}},
		{"base and impl", "enum E: int8 {\n  A = 1\n} impl {\n  f(): self {\n    return self\n  }\n}\n", []cst.Kind{cst.TypeClause, cst.EnumMember, cst.ImplBlock}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			decl := root.Children()[0].(*cst.Node)
			if decl.Kind() != cst.EnumDecl {
				t.Fatalf("first child kind = %s, want EnumDecl", decl.Kind())
			}
			got := subNodeKinds(decl)
			if len(got) != len(tc.want) {
				t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sub-nodes = %v, want %v", got, tc.want)
				}
			}
			assertLossless(t, tc.src)
		})
	}
}

func TestParseEnumMembers(t *testing.T) {
	src := "enum R: uint8 {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	names, withValue := enumMemberNames(source.NewFile("", []byte(src)), decl)
	if strings.Join(names, ",") != "Common,Rare,Legend" {
		t.Fatalf("member names = %v, want [Common Rare Legend]", names)
	}
	if withValue != 3 {
		t.Fatalf("members with initializer = %d, want 3", withValue)
	}
}

func TestParseEnumMixedSeparators(t *testing.T) {
	// Comma and newline separators may be mixed, and a member without an
	// initializer sits beside one with it.
	src := "enum E {\n  Fire, Water\n  Wind = 7\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	names, withValue := enumMemberNames(source.NewFile("", []byte(src)), decl)
	if strings.Join(names, ",") != "Fire,Water,Wind" {
		t.Fatalf("member names = %v, want [Fire Water Wind]", names)
	}
	if withValue != 1 {
		t.Fatalf("members with initializer = %d, want 1", withValue)
	}
}

func TestParseEnumDeclDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code diagnostic.Code
	}{
		{"missing name", "enum {\n  A\n}\n", CodeExpectedIdentifier},
		{"missing base type", "enum E: {\n  A\n}\n", CodeExpectedType},
		{"missing initializer value", "enum E {\n  A =\n}\n", CodeExpectedExpression},
		{"missing brace", "enum E\n", CodeUnexpectedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := Parse([]byte(tc.src))
			found := false
			for _, d := range diags {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want diagnostic %s, got %v", tc.src, tc.code, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseEnumEmpty checks that an empty enum body parses losslessly (the
// "no members" rule is a semantic concern, not a parse error).
func TestParseEnumEmpty(t *testing.T) {
	src := "enum E {\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.EnumDecl {
		t.Fatalf("first child kind = %s, want EnumDecl", decl.Kind())
	}
	if got := subNodeKinds(decl); len(got) != 0 {
		t.Fatalf("empty enum sub-nodes = %v, want none", got)
	}
	assertLossless(t, src)
}

// findFirst returns the first node of the given kind in a pre-order walk of
// root, or nil when none is present. It lets the switch tests reach into a
// function body to the SwitchStmt without spelling out every wrapper node.
func findFirst(root *cst.Node, kind cst.Kind) *cst.Node {
	if root.Kind() == kind {
		return root
	}
	for _, c := range root.Children() {
		if n, ok := c.(*cst.Node); ok {
			if found := findFirst(n, kind); found != nil {
				return found
			}
		}
	}
	return nil
}

// armNodes returns the SwitchArm children of a SwitchStmt, in order.
func armNodes(sw *cst.Node) []*cst.Node {
	var arms []*cst.Node
	for _, c := range sw.Children() {
		if n, ok := c.(*cst.Node); ok && n.Kind() == cst.SwitchArm {
			arms = append(arms, n)
		}
	}
	return arms
}

// TestParseSwitchStmt checks the shape of a parsed switch: its scrutinee, its
// arms, the value patterns and body of each arm, the multi-value and wildcard
// forms, and comma vs newline arm separators.
func TestParseSwitchStmt(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// scrutinee is the scrutinee node's kind.
		scrutinee cst.Kind
		// arms is, per arm, the kinds of its direct sub-nodes (the value
		// patterns followed by the body).
		arms [][]cst.Kind
	}{
		{
			"enum arms, newline separated",
			"pub fn c(r: R): string {\n  switch r {\n    A -> return \"a\"\n    B -> return \"b\"\n  }\n}\n",
			cst.NameRef,
			[][]cst.Kind{
				{cst.NameRef, cst.ReturnStmt},
				{cst.NameRef, cst.ReturnStmt},
			},
		},
		{
			"multi-value arm and wildcard with a block body",
			"pub fn g(n: int): string {\n  switch n {\n    0 -> return \"z\"\n    1, 2, 3 -> return \"l\"\n    _ -> {\n      return \"h\"\n    }\n  }\n}\n",
			cst.NameRef,
			[][]cst.Kind{
				{cst.Literal, cst.ReturnStmt},
				{cst.Literal, cst.Literal, cst.Literal, cst.ReturnStmt},
				{cst.NameRef, cst.Block},
			},
		},
		{
			"comma separated arms",
			"pub fn c(r: R): string {\n  switch r { A -> return \"a\", B -> return \"b\" }\n}\n",
			cst.NameRef,
			[][]cst.Kind{
				{cst.NameRef, cst.ReturnStmt},
				{cst.NameRef, cst.ReturnStmt},
			},
		},
		{
			"expression body arm",
			"pub fn c(r: R): string {\n  switch r {\n    A -> log(r)\n  }\n}\n",
			cst.NameRef,
			[][]cst.Kind{
				{cst.NameRef, cst.CallExpr},
			},
		},
		{
			"qualified-member scrutinee is not a record literal",
			"pub fn c(r: R): string {\n  switch self.x {\n    A -> return \"a\"\n  }\n}\n",
			cst.MemberExpr,
			[][]cst.Kind{
				{cst.NameRef, cst.ReturnStmt},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			sw := findFirst(root, cst.SwitchStmt)
			if sw == nil {
				t.Fatal("no SwitchStmt parsed")
			}
			sub := subNodeKinds(sw)
			if len(sub) == 0 || sub[0] != tc.scrutinee {
				t.Fatalf("scrutinee kind = %v, want %v", sub, tc.scrutinee)
			}
			arms := armNodes(sw)
			if len(arms) != len(tc.arms) {
				t.Fatalf("got %d arms, want %d", len(arms), len(tc.arms))
			}
			for i, arm := range arms {
				got := subNodeKinds(arm)
				if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.arms[i]), ",") {
					t.Fatalf("arm %d sub-nodes = %v, want %v", i, got, tc.arms[i])
				}
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseSwitchNested checks that a switch arm body may itself be a switch
// (statements compose), so the inner switch is found inside the outer arm.
func TestParseSwitchNested(t *testing.T) {
	src := "pub fn c(r: R): string {\n  switch r {\n    A -> switch r {\n      B -> return \"b\"\n    }\n  }\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	outer := findFirst(root, cst.SwitchStmt)
	if outer == nil {
		t.Fatal("no outer SwitchStmt")
	}
	arms := armNodes(outer)
	if len(arms) != 1 {
		t.Fatalf("got %d outer arms, want 1", len(arms))
	}
	if inner := findFirst(arms[0], cst.SwitchStmt); inner == nil {
		t.Fatal("arm body is not a nested SwitchStmt")
	}
	assertLossless(t, src)
}

// TestParseSwitchLossless checks that malformed and well-formed switches alike
// round-trip to the source byte for byte (the incremental pipeline's invariant).
func TestParseSwitchLossless(t *testing.T) {
	cases := []string{
		"pub fn c(r: R): string {\n  switch r {\n    A -> return \"a\"\n  }\n}\n",
		"pub fn c(r: R): string {\n  switch r { A -> return \"a\", B -> return \"b\" }\n}\n",
		"pub fn g(n: int): string {\n  switch n {\n    1, 2, 3 -> return \"l\"\n    _ -> return \"h\"\n  }\n}\n",
		"pub fn c(r: R): string {\n  switch r {\n    A -> {\n      return \"a\"\n    }\n  }\n}\n",
		"pub fn c(r: R): string {\n  switch\n}\n",                                   // missing scrutinee and body
		"pub fn c(r: R): string {\n  switch r\n}\n",                                 // missing arm block
		"pub fn c(r: R): string {\n  switch r {\n}\n}\n",                            // empty arm block
		"pub fn c(r: R): string {\n  switch r {\n    A ->\n  }\n}\n",                // arm missing body
		"pub fn c(r: R): string {\n  switch r {\n    A B -> return \"a\"\n  }\n}\n", // missing arrow
	}
	for _, src := range cases {
		assertLossless(t, src)
	}
}

// branchNodes returns the direct sub-nodes of an IfStmt: the condition, the
// then-block, and the optional else branch (a Block or a nested IfStmt).
func branchNodes(s *cst.Node) []*cst.Node {
	var out []*cst.Node
	for _, c := range s.Children() {
		if n, ok := c.(*cst.Node); ok {
			out = append(out, n)
		}
	}
	return out
}

// TestParseIfStmt checks the shape of a parsed if: its condition (suppressing
// the record-literal reading even after a binary condition), its mandatory
// then-block, and the else-omitted, else-block, and else-if chain forms.
func TestParseIfStmt(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// sub is the kinds of the IfStmt's direct sub-nodes, in order.
		sub []cst.Kind
	}{
		{
			"binary condition, no else",
			"pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
			[]cst.Kind{cst.BinaryExpr, cst.Block},
		},
		{
			"name condition, else block",
			"pub fn f(b: bool): int {\n  if b {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
			[]cst.Kind{cst.NameRef, cst.Block, cst.Block},
		},
		{
			"else-if chain",
			"pub fn f(n: int): int {\n  if n < 0 {\n    return -1\n  } else if n > 0 {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
			[]cst.Kind{cst.BinaryExpr, cst.Block, cst.IfStmt},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			s := findFirst(root, cst.IfStmt)
			if s == nil {
				t.Fatal("no IfStmt parsed")
			}
			got := subNodeKinds(s)
			if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.sub), ",") {
				t.Fatalf("if sub-nodes = %v, want %v", got, tc.sub)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseIfNested checks that an if branch body may hold further statements,
// including a nested if (the else block of sign), so control flow composes.
func TestParseIfNested(t *testing.T) {
	src := "pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  } else {\n    if n < 0 {\n      return -1\n    }\n    return 0\n  }\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	outer := findFirst(root, cst.IfStmt)
	if outer == nil {
		t.Fatal("no outer IfStmt")
	}
	branches := branchNodes(outer)
	if len(branches) != 3 || branches[2].Kind() != cst.Block {
		t.Fatalf("outer branches = %v, want condition, then-block, else-block", subNodeKinds(outer))
	}
	if inner := findFirst(branches[2], cst.IfStmt); inner == nil {
		t.Fatal("else block does not hold a nested IfStmt")
	}
	assertLossless(t, src)
}

// TestParseIfNotExpression checks that if is a statement, not an expression: a
// constant initializer that tries to use if as a value is a parse error (the
// value form of a two-way choice is the ternary, not if).
func TestParseIfNotExpression(t *testing.T) {
	_, diags := Parse([]byte("const m = if a > b { a } else { b }\n"))
	if len(diags) == 0 {
		t.Fatal("expected a parse error for if in expression position, got none")
	}
}

// TestParseIfLossless checks that malformed and well-formed ifs alike round-trip
// to the source byte for byte (the incremental pipeline's invariant).
func TestParseIfLossless(t *testing.T) {
	cases := []string{
		"pub fn f(n: int): int {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
		"pub fn f(b: bool): int {\n  if b {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
		"pub fn f(n: int): int {\n  if n < 0 {\n    return -1\n  } else if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
		"pub fn f(n: int): int {\n  if\n}\n",            // missing condition and block
		"pub fn f(n: int): int {\n  if n > 0\n}\n",      // missing then-block
		"pub fn f(n: int): int {\n  if n > 0 {\n}\n}\n", // empty then-block
		"pub fn f(n: int): int {\n  if n > 0 {} else\n}\n",
	}
	for _, src := range cases {
		assertLossless(t, src)
	}
}

// TestParseLetStmt checks the shape of a parsed let: an inferred-type binding
// (let x = e) carries an Initializer, and an annotated one (let x: T = e) carries
// a TypeClause before it — exactly the optional clauses a constant declaration's
// body uses.
func TestParseLetStmt(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// sub is the kinds of the LetStmt's direct sub-nodes, in order.
		sub []cst.Kind
	}{
		{
			"inferred type",
			"pub fn f(n: int): int {\n  let x = n\n  return x\n}\n",
			[]cst.Kind{cst.Initializer},
		},
		{
			"explicit annotation",
			"pub fn f(n: int): int {\n  let x: int = n\n  return x\n}\n",
			[]cst.Kind{cst.TypeClause, cst.Initializer},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			s := findFirst(root, cst.LetStmt)
			if s == nil {
				t.Fatal("no LetStmt parsed")
			}
			got := subNodeKinds(s)
			if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.sub), ",") {
				t.Fatalf("let sub-nodes = %v, want %v", got, tc.sub)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssignStmt checks that an identifier (or a member access) followed by
// "=" parses as an AssignStmt whose first sub-node is the target expression — the
// distinction from a bare expression statement, decided after the leading
// expression by the trailing "=".
func TestParseAssignStmt(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		target cst.Kind // the assignment target node's kind
	}{
		{
			"name target",
			"pub fn f(n: int): int {\n  let x = n\n  x = n\n  return x\n}\n",
			cst.NameRef,
		},
		{
			"member target",
			"pub fn f(n: int): int {\n  let x = n\n  x.field = n\n  return x\n}\n",
			cst.MemberExpr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			s := findFirst(root, cst.AssignStmt)
			if s == nil {
				t.Fatal("no AssignStmt parsed")
			}
			got := subNodeKinds(s)
			if len(got) != 2 || got[0] != tc.target {
				t.Fatalf("assign sub-nodes = %v, want [%v Expr]", got, tc.target)
			}
			assertLossless(t, tc.src)
		})
	}
}

// TestParseAssignNotExpression checks that assignment is a statement, not an
// expression: a "=" in a value position (a constant initializer) does not parse
// as a nested assignment — only "==" is the comparison, so a single "=" there is
// a parse error. This is the footgun guard that keeps assignment out of pure
// positions.
func TestParseAssignNotExpression(t *testing.T) {
	_, diags := Parse([]byte("const m = (a = b)\n"))
	if len(diags) == 0 {
		t.Fatal("expected a parse error for assignment in expression position, got none")
	}
}

// TestParseLetAssignLossless checks that malformed and well-formed lets and
// assignments alike round-trip to the source byte for byte.
func TestParseLetAssignLossless(t *testing.T) {
	cases := []string{
		"pub fn f(n: int): int {\n  let x = n\n  return x\n}\n",
		"pub fn f(n: int): int {\n  let x: int = n\n  x = x + 1\n  return x\n}\n",
		"pub fn f(n: int): int {\n  let\n}\n",        // missing name, clause, value
		"pub fn f(n: int): int {\n  let x\n}\n",      // missing "=" and value
		"pub fn f(n: int): int {\n  let x =\n}\n",    // missing value
		"pub fn f(n: int): int {\n  let x: int\n}\n", // annotation but no value
		"pub fn f(n: int): int {\n  x =\n}\n",        // assignment missing value
		"pub fn f(n: int): int {\n  if n > 0 {\n    let y = n\n    y = y + 1\n    return y\n  }\n  return n\n}\n",
	}
	for _, src := range cases {
		assertLossless(t, src)
	}
}
