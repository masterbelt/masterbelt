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
		"type Mapper<T, R> = fn(src: T): R\n",
		"const x = 1\ntype T = int8\npub const y = 2\n", // const/type interleaved
		"type Bad =\ntype Worse <\n",                    // malformed type decls stay lossless
		// Function literals: annotations are optional in every position.
		"const f = fn(x: int): int { return x }\n",
		"const f = fn(x) { return x }\n",
		"const f = fn(x: int, y) { return x }\n",
		"const f = fn() {}\n",
		"const f = fn(x",             // truncated literal stays lossless
		"const f = fn(x,",            // truncated after a comma (must not panic)
		"const f = fn(x,)",           // trailing comma is not part of the grammar
		"type F = fn(x: int,",        // truncated func type param list
		"type L = int8 impl {\nm(x,", // truncated method param list
		// Assert declarations.
		"assert Max > Min\n",
		"/// doc\nassert Max > Min\n",
		"const X = 1\nassert X == 1\ntype T = int8\n", // interleaved with other decls
		"assert\n",        // missing expression stays lossless
		"assert 1 >\n",    // missing right operand stays lossless
		"assert assert\n", // a keyword starts no expression (must not consume it)
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
