// Package printer renders a concrete syntax tree (package source/cst) back into
// source text.
//
// The green tree stores only widths, so the actual characters live in the buffer
// the tree was parsed from; the printer pairs the two. It is structure-driven
// for indentation: rather than echo the source's leading whitespace, it
// re-indents every line from the bracket nesting, so the indentation is a
// function of the tree, not of the input. Line breaks and inter-token spacing
// are still reproduced as they were; normalising those is the job of later
// formatting passes. Dropping all indentation and regenerating it is what makes
// the result idempotent and independent of however the input was indented.
//
// The indent model tracks the line each open bracket was opened on, not a raw
// bracket count. A line's body sits one level inside the innermost still-open
// bracket's opener line, so several brackets that open on one line and stay open
// — `.map(fn(x) {` — hug into a single level rather than stacking. A line that
// begins by closing a bracket aligns with that bracket's opener line, so the
// `})` that ends such a construct returns to the opener's column.
package printer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Print renders node and its descendants to text, re-indenting each line to its
// nesting depth with indent as one level and reading each leaf token's
// characters from buf. Newlines are emitted as "\n"; the line ending is applied
// downstream.
func Print(buf source.Buffer, node cst.Green, indent string) string {
	p := printer{indent: indent, atLineStart: true}
	p.walk(buf, cst.Root(node))
	return p.b.String()
}

// printer carries the running render state: the output, the indent unit, the
// stack of open brackets (each holding the indent level of the line it opened
// on), the current line's indent level, and whether the next significant token
// begins a line (and so needs indentation regenerated in front of it).
type printer struct {
	b           strings.Builder
	indent      string
	stack       []int
	lineIndent  int
	atLineStart bool
}

// walk renders a positioned element: a leaf goes through leaf, a node recurses
// over its children in source order.
func (p *printer) walk(buf source.Buffer, t cst.Tree) {
	if tok, ok := t.Token(); ok {
		p.leaf(tok.Kind(), t.Text(buf))
		return
	}
	for _, child := range t.Children() {
		p.walk(buf, child)
	}
}

// leaf renders one token, normalising indentation. A newline opens a new line; a
// run of whitespace is kept between tokens but dropped at the start of a line
// (where it was indentation, now regenerated). Any other token, when it starts a
// line, is preceded by the line's indent: a leading closer aligns with its
// opener line (and pops it), otherwise the line sits one level inside the
// innermost open bracket. An opener records the line it opened on, so brackets
// opening together on one line hug into a single level.
func (p *printer) leaf(kind token.Kind, text string) {
	switch kind {
	case token.Newline:
		p.b.WriteByte('\n')
		p.atLineStart = true
		return
	case token.Whitespace:
		if !p.atLineStart {
			p.b.WriteString(text)
		}
		return
	}

	leadingCloser := p.atLineStart && isClose(kind) && len(p.stack) > 0
	if p.atLineStart {
		switch {
		case leadingCloser:
			p.lineIndent = p.pop()
		case len(p.stack) > 0:
			p.lineIndent = p.stack[len(p.stack)-1] + 1
		default:
			p.lineIndent = 0
		}
		p.b.WriteString(strings.Repeat(p.indent, p.lineIndent))
		p.atLineStart = false
	} else if isClose(kind) && len(p.stack) > 0 {
		p.pop()
	}

	if isOpen(kind) {
		p.stack = append(p.stack, p.lineIndent)
	}
	p.b.WriteString(text)
}

// pop removes the innermost open bracket and returns the indent level of the
// line it opened on.
func (p *printer) pop() int {
	top := p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	return top
}

// isOpen reports whether the token opens an indented region.
func isOpen(k token.Kind) bool {
	return k == token.LBrace || k == token.LBracket || k == token.LParen
}

// isClose reports whether the token closes an indented region.
func isClose(k token.Kind) bool {
	return k == token.RBrace || k == token.RBracket || k == token.RParen
}
