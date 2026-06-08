// Package printer renders a concrete syntax tree (package source/cst) back into
// source text.
//
// The green tree stores only widths, so the actual characters live in the buffer
// the tree was parsed from; the printer pairs the two. It is structure-driven:
// rather than echo the source's whitespace, it regenerates indentation from the
// bracket nesting and the space between tokens from the grammar, so the layout
// is a function of the tree, not of the input. Line breaks are still reproduced
// as they were (normalising those is a later pass), and a trailing comment is
// set off from the code before it by a single space rather than whatever
// alignment the input used. Regenerating all spacing is what makes the result
// idempotent and independent of however the input was spaced.
//
// Indentation tracks the line each open bracket was opened on, not a raw bracket
// count, so brackets that open together and stay open — `.map(fn(x) {` — hug
// into a single level, and a line that begins by closing a bracket aligns with
// its opener line. Inter-token spacing is decided per token pair by
// spaceBetween, using each token's immediate parent node to disambiguate the
// context-sensitive cases (a type colon versus a ternary colon, a unary versus a
// binary operator, a generic bracket versus a comparison, a record brace versus
// a block brace).
package printer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Print renders node and its descendants to text, regenerating indentation (one
// level of indent per nesting depth) and inter-token spacing, and reading each
// leaf token's characters from buf. Newlines are emitted as "\n"; the line
// ending is applied downstream.
func Print(buf source.Buffer, node cst.Green, indent string) string {
	p := printer{indent: indent, atLineStart: true}
	p.walk(buf, cst.Root(node), cst.File)
	return p.b.String()
}

// printer carries the running render state: the output and indent unit; the
// stack of open brackets (each holding the indent level of the line it opened
// on) and the current line's indent level; whether the next token begins a line;
// and the previous token's kind, parent, and whether it was a comment (all for
// deciding the inter-token spacing).
type printer struct {
	b           strings.Builder
	indent      string
	stack       []int
	lineIndent  int
	atLineStart bool
	prevKind    token.Kind
	prevParent  cst.Kind
	prevComment bool
}

// walk renders a positioned element: a leaf goes through leaf with its immediate
// parent's kind, a node recurses over its children as their parent.
func (p *printer) walk(buf source.Buffer, t cst.Tree, parent cst.Kind) {
	if tok, ok := t.Token(); ok {
		p.leaf(tok.Kind(), parent, t.Text(buf))
		return
	}
	kind, _ := t.Kind()
	for _, child := range t.Children() {
		p.walk(buf, child, kind)
	}
}

// leaf renders one token. A newline opens a new line; whitespace is dropped (all
// spacing is regenerated). A significant token at the start of a line is preceded
// by the regenerated indent; mid-line it is preceded by a single space or none
// per spaceBetween — except next to a comment, which a single space always sets
// off from the code on its line.
func (p *printer) leaf(kind token.Kind, parent cst.Kind, text string) {
	switch kind {
	case token.Newline:
		p.b.WriteByte('\n')
		p.atLineStart = true
		return
	case token.Whitespace:
		return
	}

	comment := isComment(kind)

	if p.atLineStart {
		leadingCloser := !comment && isClose(kind) && len(p.stack) > 0
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
	} else {
		switch {
		case comment || p.prevComment:
			p.b.WriteByte(' ')
		case spaceBetween(p.prevKind, kind, p.prevParent, parent):
			p.b.WriteByte(' ')
		}
		if !comment && isClose(kind) && len(p.stack) > 0 {
			p.pop()
		}
	}

	if !comment && isOpen(kind) {
		p.stack = append(p.stack, p.lineIndent)
	}

	p.b.WriteString(text)
	p.prevKind = kind
	p.prevParent = parent
	p.prevComment = comment
}

// pop removes the innermost open bracket and returns the indent level of the
// line it opened on.
func (p *printer) pop() int {
	top := p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	return top
}

// spaceBetween reports whether a single space separates two adjacent significant
// tokens on one line. The default is a space; the cases below are where the
// canonical spelling hugs instead. prev/cur are the token kinds, pp/cp their
// immediate parent node kinds — the parent is what tells a type colon from a
// ternary colon, a unary from a binary operator, a generic bracket from a
// comparison, and a record brace from a block brace.
func spaceBetween(prev, cur token.Kind, pp, cp cst.Kind) bool {
	switch {
	// A comma or a closing bracket hugs what precedes it; an opening paren or
	// bracket hugs what follows it.
	case cur == token.Comma, cur == token.RParen, cur == token.RBracket:
		return false
	case prev == token.LParen, prev == token.LBracket:
		return false
	// Member access and the range operators bind tight on both sides.
	case cur == token.Dot, prev == token.Dot:
		return false
	case cur == token.DotDot, cur == token.DotDotDot, prev == token.DotDot, prev == token.DotDotDot:
		return false
	}

	// Record braces carry inner spaces ("{ x: 0 }"), except an empty record is
	// tight ("{}") and a record's type name hugs its brace ("Point{").
	if cur == token.LBrace && isRecord(cp) {
		return !(prev == token.Ident && pp == cst.RecordLit)
	}
	if prev == token.LBrace && isRecord(pp) {
		return cur != token.RBrace
	}
	if cur == token.RBrace && isRecord(cp) {
		return prev != token.LBrace
	}

	switch {
	// A call/parameter "(" and an index "[" hug their head.
	case cur == token.LParen && (cp == cst.CallExpr || cp == cst.ParamList):
		return false
	case cur == token.LBracket && cp == cst.IndexExpr:
		return false
	// A type, key, or return colon has no space before it — only after.
	case cur == token.Colon && cp != cst.TernaryExpr:
		return false
	// A unary operator hugs its operand.
	case (prev == token.Plus || prev == token.Minus || prev == token.Bang) && pp == cst.UnaryExpr:
		return false
	// Generic brackets hug their contents and their head.
	case cur == token.Lt && isGeneric(cp):
		return false
	case prev == token.Lt && isGeneric(pp):
		return false
	case cur == token.Gt && isGeneric(cp):
		return false
	}
	return true
}

// isOpen reports whether the token opens an indented/bracketed region.
func isOpen(k token.Kind) bool {
	return k == token.LBrace || k == token.LBracket || k == token.LParen
}

// isClose reports whether the token closes an indented/bracketed region.
func isClose(k token.Kind) bool {
	return k == token.RBrace || k == token.RBracket || k == token.RParen
}

// isComment reports whether the token is a comment (line, block, or doc).
func isComment(k token.Kind) bool {
	return k == token.LineComment || k == token.BlockComment || k == token.DocComment
}

// isRecord reports whether the node is a record literal or record type — the
// braces that carry inner spaces.
func isRecord(k cst.Kind) bool {
	return k == cst.RecordLit || k == cst.RecordType
}

// isGeneric reports whether the node is a generic argument or parameter list —
// the angle brackets that hug.
func isGeneric(k cst.Kind) bool {
	return k == cst.GenericArgs || k == cst.GenericParams
}
