// This file tests the parsing of expressions and statements — literals, the
// operator grammar, blocks, and the let/assign/if/for/switch/match forms —
// mirroring parser_expr.go.
package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

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
	return nil, cst.Tree{} // unreachable: Fatalf stops the test
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
// funcLitCases drives TestParseFuncLit.
var funcLitCases = []struct {
	name string
	src  string
	want []cst.Kind   // FuncLit's direct sub-nodes
	prm  [][]cst.Kind // each Param's sub-nodes (annotation present or not)
}{
	{
		"fully annotated", "const f = fn(x: nint): nint { return x }\n",
		[]cst.Kind{cst.ParamList, cst.TypeName, cst.Block},
		[][]cst.Kind{{cst.TypeName}},
	},
	{
		"no result", "const f = fn(x: nint) { return x }\n",
		[]cst.Kind{cst.ParamList, cst.Block},
		[][]cst.Kind{{cst.TypeName}},
	},
	{
		"no annotations", "const f = fn(x) { return x }\n",
		[]cst.Kind{cst.ParamList, cst.Block},
		[][]cst.Kind{nil},
	},
	{
		"partially annotated", "const f = fn(x: nint, y) { return x }\n",
		[]cst.Kind{cst.ParamList, cst.Block},
		[][]cst.Kind{{cst.TypeName}, nil},
	},
	{
		"result only", "const f = fn(x): nint { return x }\n",
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
		"arrow annotated param", "const f = fn(x: nint) -> x * 3\n",
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
		"arrow with result", "const f = fn(x): nint -> x\n",
		[]cst.Kind{cst.ParamList, cst.TypeName, cst.NameRef},
		[][]cst.Kind{nil},
	},
	{
		"arrow nested", "const f = fn(x) -> fn(y) -> y\n",
		[]cst.Kind{cst.ParamList, cst.FuncLit},
		[][]cst.Kind{nil},
	},
}

func TestParseFuncLit(t *testing.T) {
	for _, tc := range funcLitCases {
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
			assertParamKinds(t, funcLitParams(e), tc.prm)
		})
	}
}

// funcLitParams returns the Param trees of a FuncLit tree, drawn from its
// ParamList child.
func funcLitParams(e cst.Tree) []cst.Tree {
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
	return params
}

// assertParamKinds checks each parsed param's sub-node kinds against want (one
// expected kind list per param), failing on a count or shape mismatch.
func assertParamKinds(t *testing.T, params []cst.Tree, want [][]cst.Kind) {
	t.Helper()
	if len(params) != len(want) {
		t.Fatalf("got %d params, want %d", len(params), len(want))
	}
	for i, p := range params {
		pn, _ := p.Node()
		got := subNodeKinds(pn)
		if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(want[i]), ",") {
			t.Fatalf("param %d sub-nodes = %v, want %v", i, got, want[i])
		}
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

func TestParseAwaitExpr(t *testing.T) {
	src := "fn io async f(u: string): string {\n  return await g(u).trim()\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	assertLossless(t, src)
	tree := cst.Sprint(root)
	if !strings.Contains(tree, "AwaitExpr") {
		t.Errorf("tree = %s, want an AwaitExpr node", tree)
	}

	// A dangling await reports a missing operand.
	if _, diags := Parse([]byte("const x = await\n")); len(diags) == 0 {
		t.Errorf("dangling await: want a diagnostic")
	}
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

// switchStmtCases drives TestParseSwitchStmt.
var switchStmtCases = []struct {
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
		"pub fn g(n: nint): string {\n  switch n {\n    0 -> return \"z\"\n    1, 2, 3 -> return \"l\"\n    _ -> {\n      return \"h\"\n    }\n  }\n}\n",
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

// TestParseSwitchStmt checks the shape of a parsed switch: its scrutinee, its
// arms, the value patterns and body of each arm, the multi-value and wildcard
// forms, and comma vs newline arm separators.
func TestParseSwitchStmt(t *testing.T) {
	for _, tc := range switchStmtCases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			sw := findFirst(root, cst.SwitchStmt)
			if sw == nil {
				t.Fatal("no SwitchStmt parsed")
			}
			assertSwitchShape(t, sw, tc.scrutinee, tc.arms)
			assertLossless(t, tc.src)
		})
	}
}

// assertSwitchShape checks a parsed switch node: its scrutinee kind, its arm
// count, and each arm's sub-node kinds (value patterns followed by the body).
func assertSwitchShape(t *testing.T, sw *cst.Node, scrutinee cst.Kind, wantArms [][]cst.Kind) {
	t.Helper()
	sub := subNodeKinds(sw)
	if len(sub) == 0 || sub[0] != scrutinee {
		t.Fatalf("scrutinee kind = %v, want %v", sub, scrutinee)
	}
	arms := armNodes(sw)
	if len(arms) != len(wantArms) {
		t.Fatalf("got %d arms, want %d", len(arms), len(wantArms))
	}
	for i, arm := range arms {
		got := subNodeKinds(arm)
		if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(wantArms[i]), ",") {
			t.Fatalf("arm %d sub-nodes = %v, want %v", i, got, wantArms[i])
		}
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
		"pub fn g(n: nint): string {\n  switch n {\n    1, 2, 3 -> return \"l\"\n    _ -> return \"h\"\n  }\n}\n",
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
			"pub fn f(n: nint): nint {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
			[]cst.Kind{cst.BinaryExpr, cst.Block},
		},
		{
			"name condition, else block",
			"pub fn f(b: bool): nint {\n  if b {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
			[]cst.Kind{cst.NameRef, cst.Block, cst.Block},
		},
		{
			"else-if chain",
			"pub fn f(n: nint): nint {\n  if n < 0 {\n    return -1\n  } else if n > 0 {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
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
	src := "pub fn f(n: nint): nint {\n  if n > 0 {\n    return 1\n  } else {\n    if n < 0 {\n      return -1\n    }\n    return 0\n  }\n}\n"
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
		"pub fn f(n: nint): nint {\n  if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
		"pub fn f(b: bool): nint {\n  if b {\n    return 1\n  } else {\n    return 0\n  }\n}\n",
		"pub fn f(n: nint): nint {\n  if n < 0 {\n    return -1\n  } else if n > 0 {\n    return 1\n  }\n  return 0\n}\n",
		"pub fn f(n: nint): nint {\n  if\n}\n",            // missing condition and block
		"pub fn f(n: nint): nint {\n  if n > 0\n}\n",      // missing then-block
		"pub fn f(n: nint): nint {\n  if n > 0 {\n}\n}\n", // empty then-block
		"pub fn f(n: nint): nint {\n  if n > 0 {} else\n}\n",
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
			"pub fn f(n: nint): nint {\n  let x = n\n  return x\n}\n",
			[]cst.Kind{cst.Initializer},
		},
		{
			"explicit annotation",
			"pub fn f(n: nint): nint {\n  let x: nint = n\n  return x\n}\n",
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
			"pub fn f(n: nint): nint {\n  let x = n\n  x = n\n  return x\n}\n",
			cst.NameRef,
		},
		{
			"member target",
			"pub fn f(n: nint): nint {\n  let x = n\n  x.field = n\n  return x\n}\n",
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
		"pub fn f(n: nint): nint {\n  let x = n\n  return x\n}\n",
		"pub fn f(n: nint): nint {\n  let x: nint = n\n  x = x + 1\n  return x\n}\n",
		"pub fn f(n: nint): nint {\n  let\n}\n",         // missing name, clause, value
		"pub fn f(n: nint): nint {\n  let x\n}\n",       // missing "=" and value
		"pub fn f(n: nint): nint {\n  let x =\n}\n",     // missing value
		"pub fn f(n: nint): nint {\n  let x: nint\n}\n", // annotation but no value
		"pub fn f(n: nint): nint {\n  x =\n}\n",         // assignment missing value
		"pub fn f(n: nint): nint {\n  if n > 0 {\n    let y = n\n    y = y + 1\n    return y\n  }\n  return n\n}\n",
	}
	for _, src := range cases {
		assertLossless(t, src)
	}
}
