package printer

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// printLeaves renders a flat run of leaf tokens. The printer flattens the tree and
// tracks depth by bracket tokens, so a flat File of leaves exercises the layout
// logic without building real nodes. The buffer is the in-order concatenation
// of the leaf texts, which is what the positioned tree reads through.
func printLeaves(t *testing.T, indent string, leaves ...cst.Green) string {
	t.Helper()
	root := cst.NewNode(cst.File, leaves)
	var src strings.Builder
	for _, l := range leaves {
		src.WriteString(l.(*cst.Token).Text())
	}
	return Print(source.NewFile("", []byte(src.String())), root, indent)
}

func tok(k token.Kind, text string) cst.Green { return cst.NewToken(k, text) }

// printTree renders a green tree whose buffer is the in-order concatenation of
// its leaf texts. It is the node-aware counterpart to printLeaves, for the layout
// rules (literal collapsing) that depend on real node kinds.
func printTree(t *testing.T, indent string, root cst.Green) string {
	t.Helper()
	return Print(source.NewFile("", []byte(treeText(root))), root, indent)
}

// treeText reconstructs a green tree's source: the concatenation of its leaf
// token texts, which is exactly what the positioned tree reads by offset.
func treeText(g cst.Green) string {
	switch n := g.(type) {
	case *cst.Token:
		return n.Text()
	case *cst.Node:
		var b strings.Builder
		for _, c := range n.Children() {
			b.WriteString(treeText(c))
		}
		return b.String()
	}
	return ""
}

// lit wraps an integer literal token in a Literal node, the shape a collection
// element has in a real tree.
func lit(n string) cst.Green {
	return cst.NewNode(cst.Literal, []cst.Green{tok(token.Int, n)})
}

// collection builds a CollectionLit of integer-literal elements. With sep set to
// a newline the list is multi-line (the magic-trailing-comma signal); with a
// comma it is a one-line list. Trailing separators land before the "]" so the
// renderer's separator regeneration is exercised.
func collection(sep token.Kind, sepText string, elems ...string) cst.Green {
	// "[", then a (sep, lit) pair per element, then a trailing sep and "]".
	children := make([]cst.Green, 0, 1+2*len(elems)+2)
	children = append(children, tok(token.LBracket, "["))
	for _, e := range elems {
		children = append(children, tok(sep, sepText), lit(e))
	}
	children = append(children, tok(sep, sepText), tok(token.RBracket, "]"))
	return cst.NewNode(cst.File, []cst.Green{cst.NewNode(cst.CollectionLit, children)})
}

func TestPrintExpandsMultilineList(t *testing.T) {
	// A list the author broke across lines stays one element per line, each with
	// a synthesized trailing comma, re-indented to its bracket depth.
	want := "[\n  1,\n  2,\n  3,\n]"
	if got := printTree(t, "  ", collection(token.Newline, "\n", "1", "2", "3")); got != want {
		t.Errorf("Print = %q, want %q", got, want)
	}
}

func TestPrintFlattensInlineList(t *testing.T) {
	// A one-line list stays inline, comma-separated, with no trailing comma — the
	// source trailing comma (the sep before "]") is dropped.
	if got := printTree(t, "  ", collection(token.Comma, ",", "1", "2", "3")); got != "[1, 2, 3]" {
		t.Errorf("Print = %q, want %q", got, "[1, 2, 3]")
	}
}

func TestPrintNormalizesInterTokenSpace(t *testing.T) {
	// Two adjacent words get a single regenerated space regardless of the input
	// spacing. (Context-sensitive spacing — colons, generics, records, operators
	// — is exercised by the parsed fixtures, where parent kinds are real; a flat
	// hand-built tree has only File parents.)
	got := printLeaves(t, "  ",
		tok(token.Ident, "ab"),
		tok(token.Whitespace, "   "),
		tok(token.Ident, "cd"),
	)
	if got != "ab cd" {
		t.Errorf("Print = %q, want %q", got, "ab cd")
	}
}

func TestPrintReindentsToDepth(t *testing.T) {
	// A messily indented block body is re-indented to its bracket depth, and the
	// closing brace dedents to the opener's level.
	leaves := []cst.Green{
		tok(token.LBrace, "{"),
		tok(token.Newline, "\n"),
		tok(token.Whitespace, "      "), // 6 spaces of nonsense
		tok(token.Ident, "x"),
		tok(token.Newline, "\n"),
		tok(token.RBrace, "}"),
	}
	if got := printLeaves(t, "  ", leaves...); got != "{\n  x\n}" {
		t.Errorf("two-space: Print = %q, want %q", got, "{\n  x\n}")
	}
	if got := printLeaves(t, "\t", leaves...); got != "{\n\tx\n}" {
		t.Errorf("tab: Print = %q, want %q", got, "{\n\tx\n}")
	}
}

func TestPrintNestedDepth(t *testing.T) {
	// Two bracket levels indent twice; each closer dedents one level.
	leaves := []cst.Green{
		tok(token.LBrace, "{"),
		tok(token.Newline, "\n"),
		tok(token.LBracket, "["),
		tok(token.Newline, "\n"),
		tok(token.Ident, "x"),
		tok(token.Newline, "\n"),
		tok(token.RBracket, "]"),
		tok(token.Newline, "\n"),
		tok(token.RBrace, "}"),
	}
	want := "{\n  [\n    x\n  ]\n}"
	if got := printLeaves(t, "  ", leaves...); got != want {
		t.Errorf("Print = %q, want %q", got, want)
	}
}

func TestPrintHugsBracketsOpenedTogether(t *testing.T) {
	// Two brackets that open on one line and stay open ("[{") indent the body by
	// a single level, not two; the trailing "}]" returns to the opener line.
	leaves := []cst.Green{
		tok(token.LBracket, "["),
		tok(token.LBrace, "{"),
		tok(token.Newline, "\n"),
		tok(token.Ident, "x"),
		tok(token.Newline, "\n"),
		tok(token.RBrace, "}"),
		tok(token.RBracket, "]"),
	}
	if got := printLeaves(t, "  ", leaves...); got != "[{\n  x\n}]" {
		t.Errorf("Print = %q, want %q", got, "[{\n  x\n}]")
	}
}

func TestPrintLeavesBlankLinesEmpty(t *testing.T) {
	// A blank line carries no regenerated indentation, so nothing trails on it.
	leaves := []cst.Green{
		tok(token.LBrace, "{"),
		tok(token.Newline, "\n"),
		tok(token.Newline, "\n"),
		tok(token.Ident, "x"),
		tok(token.Newline, "\n"),
		tok(token.RBrace, "}"),
	}
	if got := printLeaves(t, "  ", leaves...); got != "{\n\n  x\n}" {
		t.Errorf("Print = %q, want %q", got, "{\n\n  x\n}")
	}
}
