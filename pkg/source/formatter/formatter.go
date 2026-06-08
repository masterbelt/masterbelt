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
// newline and the line terminator — while the real formatting rules are still
// being decided. The shape is the point: callers format a parsed tree through
// here, and the policy can grow in this package (and the printer) without
// changing them.
//
// A Layout carries the per-project substrate (indent unit, line terminator) an
// .editorconfig owns; everything else is masterbelt's and not configurable.
package formatter

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/printer"
)

// Format returns the canonical formatting of the file rooted at root, reading
// token text from buf and laying lines out under layout: the printer re-indents
// with layout.Indent, and normalize renders the line breaks as layout.EndOfLine.
func Format(buf source.Buffer, root cst.Green, layout Layout) string {
	return normalize(printer.Print(buf, root, layout.Indent), layout)
}

// normalize trims trailing whitespace from every line, collapses every run of
// blank lines to a single blank line (and drops leading and trailing blanks),
// ensures exactly one trailing line break (and empty output when there is no
// content), and renders every break as layout.EndOfLine. It splits on "\n" and
// trims a dangling "\r" from each line, so any input line ending is folded to
// the layout's before being re-emitted.
func normalize(text string, layout Layout) string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	pendingBlank := false
	for _, line := range raw {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			pendingBlank = true
			continue
		}
		// A blank line is emitted only between content lines, and at most one
		// per run — so leading and trailing blanks vanish and any longer gap
		// collapses to a single blank line.
		if pendingBlank && len(lines) > 0 {
			lines = append(lines, "")
		}
		pendingBlank = false
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "\n") + "\n"
	if eol := layout.EndOfLine; eol != "" && eol != "\n" {
		out = strings.ReplaceAll(out, "\n", eol)
	}
	return out
}
