package printer

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

func TestPrintReproducesSource(t *testing.T) {
	src := "ab cd"
	buf := source.NewFile("", []byte(src))

	// Leaves tile [0,5): "ab"(2) + " "(1) + "cd"(2). The kinds are irrelevant to
	// printing; only the widths (and the buffer) matter.
	root := cst.NewNode(cst.File, []cst.Green{
		cst.NewToken(token.Ident, "ab"),
		cst.NewToken(token.Whitespace, " "),
		cst.NewToken(token.Ident, "cd"),
	})

	if got := Print(buf, root); got != src {
		t.Errorf("Print = %q, want %q", got, src)
	}
}
