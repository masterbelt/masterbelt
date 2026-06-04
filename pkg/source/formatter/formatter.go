// Package formatter produces the canonical formatting of masterbelt source.
//
// It formats the concrete syntax tree (package source/cst), not the abstract
// tree: formatting must preserve comments and other trivia, which only the
// lossless concrete tree retains. The tree's characters live in the buffer it
// was parsed from, so Format takes both — the same (buffer, tree) pair the
// printer needs, and not the parser's Document, so that this package stays in
// the source layer and never depends on the parser.
//
// The policy is deliberately minimal for now — the printer reproduces the tree
// faithfully and Format only trims trailing whitespace and normalises the final
// newline — while the real formatting rules are still being decided. The shape
// is the point: callers format a parsed tree through here, and the policy can
// grow in this package (and the printer) without changing them.
package formatter

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/printer"
)

// Format returns the canonical formatting of the file rooted at root, reading
// token text from buf.
func Format(buf source.Buffer, root cst.Green) string {
	return normalize(printer.Print(buf, root))
}

// normalize trims trailing whitespace from every line and ensures the text ends
// with exactly one newline (and is empty when there is no content).
func normalize(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if out != "" {
		out += "\n"
	}
	return out
}
