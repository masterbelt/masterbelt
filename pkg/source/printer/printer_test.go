package printer

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// print renders a flat run of leaf tokens. The printer flattens the tree and
// tracks depth by bracket tokens, so a flat File of leaves exercises the layout
// logic without building real nodes. The buffer is the in-order concatenation
// of the leaf texts, which is what the positioned tree reads through.
func print(t *testing.T, indent string, leaves ...cst.Green) string {
	t.Helper()
	root := cst.NewNode(cst.File, leaves)
	var src strings.Builder
	for _, l := range leaves {
		src.WriteString(l.(*cst.Token).Text())
	}
	return Print(source.NewFile("", []byte(src.String())), root, indent)
}

func tok(k token.Kind, text string) cst.Green { return cst.NewToken(k, text) }

func TestPrintPreservesInlineTokens(t *testing.T) {
	// No newline and no bracket: inter-token spacing is kept verbatim (spacing
	// is a later pass), and a single-line run is unchanged.
	got := print(t, "  ",
		tok(token.Ident, "ab"),
		tok(token.Whitespace, " "),
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
	if got := print(t, "  ", leaves...); got != "{\n  x\n}" {
		t.Errorf("two-space: Print = %q, want %q", got, "{\n  x\n}")
	}
	if got := print(t, "\t", leaves...); got != "{\n\tx\n}" {
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
	if got := print(t, "  ", leaves...); got != want {
		t.Errorf("Print = %q, want %q", got, want)
	}
}

func TestPrintHugsBracketsOpenedTogether(t *testing.T) {
	// Two brackets that open on one line and stay open ("a({") indent the body
	// by a single level, not two; the trailing "})" returns to the opener line.
	leaves := []cst.Green{
		tok(token.Ident, "a"),
		tok(token.LParen, "("),
		tok(token.LBrace, "{"),
		tok(token.Newline, "\n"),
		tok(token.Ident, "x"),
		tok(token.Newline, "\n"),
		tok(token.RBrace, "}"),
		tok(token.RParen, ")"),
	}
	if got := print(t, "  ", leaves...); got != "a({\n  x\n})" {
		t.Errorf("Print = %q, want %q", got, "a({\n  x\n})")
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
	if got := print(t, "  ", leaves...); got != "{\n\n  x\n}" {
		t.Errorf("Print = %q, want %q", got, "{\n\n  x\n}")
	}
}
