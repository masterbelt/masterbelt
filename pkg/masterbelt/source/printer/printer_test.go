package printer

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

func TestPrintReproducesSource(t *testing.T) {
	src := "ab cd"
	buf := source.NewFile("", []byte(src))

	// Leaves tile [0,5): "ab"(2) + " "(1) + "cd"(2). The kinds are irrelevant to
	// printing; only the widths (and the buffer) matter.
	root := cst.NewNode(cst.File, []cst.Green{
		cst.NewToken(token.Ident, 2),
		cst.NewToken(token.Whitespace, 1),
		cst.NewToken(token.Ident, 2),
	})

	if got := Print(buf, root); got != src {
		t.Errorf("Print = %q, want %q", got, src)
	}
}
