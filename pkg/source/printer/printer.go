// Package printer renders a concrete syntax tree (package source/cst) back into
// source text.
//
// The green tree stores only widths, so the actual characters live in the buffer
// the tree was parsed from; the printer pairs the two. Today it prints
// faithfully — concatenating every leaf token's text reproduces the source byte
// for byte — which makes it the mechanism a formatter builds on and the natural
// home for canonical layout rules once they are defined.
package printer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// Print renders node and its descendants to text, reading each leaf token's
// characters from buf. Printing a whole File reproduces its source exactly.
func Print(buf source.Buffer, node cst.Green) string {
	var b strings.Builder
	write(&b, buf, cst.Root(node))
	return b.String()
}

func write(b *strings.Builder, buf source.Buffer, t cst.Tree) {
	if _, ok := t.Token(); ok {
		b.WriteString(t.Text(buf))
		return
	}
	for _, child := range t.Children() {
		write(b, buf, child)
	}
}
