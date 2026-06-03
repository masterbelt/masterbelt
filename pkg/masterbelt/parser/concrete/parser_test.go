package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
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
		{"const x = 1 + 2 * 3\n", "(1 + (2 * 3))"},     // * binds tighter than +
		{"const x = 1 * 2 + 3\n", "((1 * 2) + 3)"},
		{"const x = 1 - 2 - 3\n", "((1 - 2) - 3)"},     // left-associative
		{"const x = 1 < 2 && 3 > 4\n", "((1 < 2) && (3 > 4))"},
		{"const x = a || b && c\n", "(a || (b && c))"}, // && binds tighter than ||
		{"const x = -1 + 2\n", "((- 1) + 2)"},          // unary binds tightest
		{"const x = !a && b\n", "((! a) && b)"},
		{"const x = - - 1\n", "(- (- 1))"},
		{"const x = true\n", "true"},
		{"const x = false || true\n", "(false || true)"},
	}
	for _, tc := range cases {
		buf, e := initExpr(t, tc.src)
		if got := exprSexpr(buf, e); got != tc.want {
			t.Errorf("%q: shape = %s, want %s", tc.src, got, tc.want)
		}
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
