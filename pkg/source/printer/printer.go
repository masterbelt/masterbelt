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
//
// A comma-separated list (a record or collection literal, a call's arguments, a
// parameter list, a generic argument or parameter list, a use list) keeps the
// line-break choice the input made — the "magic trailing comma" rule, chosen for
// minimal diffs: a list the author wrote across lines stays one element per line,
// each ending with a comma (so adding an element is a one-line diff); a list on
// one line stays inline, separated by ", " with no trailing comma. The signal is
// a newline directly between the brackets, so a one-line call whose only argument
// is a multi-line lambda stays inline (the lambda's own breaks are its business).
// Either way the separators are regenerated — source commas are dropped, the
// trailing one synthesized only when multi-line — so a list has one form for its
// chosen shape. A list carrying a comment is left exactly as written, since
// moving its elements could strand the comment.
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
// parent's kind; a node recurses over its children as their parent, except a
// comma-separated list, which renders through commaList.
func (p *printer) walk(buf source.Buffer, t cst.Tree, parent cst.Kind) {
	if tok, ok := t.Token(); ok {
		p.leaf(tok.Kind(), parent, t.Text(buf))
		return
	}
	kind, _ := t.Kind()
	if kind == cst.FuncLit {
		if expr, ok := arrowBody(t); ok {
			p.funcLitArrow(buf, t, expr)
			return
		}
	}
	if isCommaList(kind) {
		p.commaList(buf, t, kind)
		return
	}
	for _, child := range t.Children() {
		p.walk(buf, child, kind)
	}
}

// funcLitArrow renders a lambda whose block body is a single value-returning
// statement in the arrow shorthand: the head (fn, parameters, optional return
// type) as written, then "-> " and the returned expression in place of the
// "{ return E }" block. The two forms are semantically identical, so this is a
// spelling choice.
func (p *printer) funcLitArrow(buf source.Buffer, funcLit cst.Tree, expr cst.Tree) {
	for _, child := range funcLit.Children() {
		if k, ok := child.Kind(); ok && k == cst.Block {
			break // the block is replaced by the arrow body
		}
		p.walk(buf, child, cst.FuncLit)
	}
	p.leaf(token.Arrow, cst.FuncLit, "->")
	p.walk(buf, expr, cst.FuncLit)
}

// arrowBody returns the returned expression when funcLit's body is a block of
// exactly one "return E" statement and nothing else — no other statements, no
// comment whose line the shorthand would drop — and reports false otherwise (a
// lambda already in arrow form, a multi-statement body, a bare return, or a
// commented one), in which case the body is rendered as written.
func arrowBody(funcLit cst.Tree) (cst.Tree, bool) {
	block, ok := childOfKind(funcLit, cst.Block)
	if !ok {
		return cst.Tree{}, false // already the arrow form
	}
	var ret cst.Tree
	returns := 0
	for _, child := range block.Children() {
		if ck, isToken := child.TokenKind(); isToken {
			switch ck {
			case token.LBrace, token.RBrace, token.Whitespace, token.Newline:
				continue
			default:
				return cst.Tree{}, false // a comment or stray token: keep the block
			}
		}
		if k, _ := child.Kind(); k != cst.ReturnStmt {
			return cst.Tree{}, false // some other statement: keep the block
		}
		ret = child
		returns++
	}
	if returns != 1 {
		return cst.Tree{}, false
	}
	return returnValue(ret)
}

// returnValue returns the expression of a "return E" statement, or false when it
// is a bare return or carries a comment.
func returnValue(ret cst.Tree) (cst.Tree, bool) {
	var expr cst.Tree
	values := 0
	for _, child := range ret.Children() {
		if ck, isToken := child.TokenKind(); isToken {
			if ck == token.Return || ck == token.Whitespace || ck == token.Newline {
				continue
			}
			return cst.Tree{}, false // a comment or stray token
		}
		expr = child
		values++
	}
	if values != 1 {
		return cst.Tree{}, false
	}
	return expr, true
}

// childOfKind returns the first child node of the given kind.
func childOfKind(t cst.Tree, kind cst.Kind) (cst.Tree, bool) {
	for _, child := range t.Children() {
		if k, ok := child.Kind(); ok && k == kind {
			return child, true
		}
	}
	return cst.Tree{}, false
}

// commaList renders a comma-separated list under the magic-trailing-comma rule.
// It walks the prefix (a record's type name, a call's callee) and the opening
// bracket, then the region between the brackets one of three ways, then the
// closing bracket and any suffix. A region with a comment is left as written (so
// the comment keeps its place); a region the author broke across lines (a
// newline sits directly between the brackets) is expanded one element per line
// with a trailing comma; otherwise it is flattened inline.
func (p *printer) commaList(buf source.Buffer, t cst.Tree, kind cst.Kind) {
	children := t.Children()
	openIdx, closeIdx := bracketBounds(children)
	if openIdx < 0 || closeIdx <= openIdx {
		for _, child := range children { // malformed: render faithfully
			p.walk(buf, child, kind)
		}
		return
	}
	for _, child := range children[:openIdx+1] { // prefix and "(" / "{" / "[" / "<"
		p.walk(buf, child, kind)
	}
	region := children[openIdx+1 : closeIdx]
	switch {
	case regionHasComment(region):
		for _, child := range region {
			p.walk(buf, child, kind)
		}
	case regionIsMultiline(region):
		p.expandRegion(buf, region, kind)
		p.leaf(token.Newline, kind, "\n")
	default:
		p.flatRegion(buf, region, kind)
	}
	for _, child := range children[closeIdx:] { // ")" / "}" / "]" / ">" and any suffix
		p.walk(buf, child, kind)
	}
}

// flatRegion renders the elements inline, separated by a synthesized comma (the
// source commas, trailing one included, are dropped). Trivia is skipped; an
// element's own internal line breaks — a multi-line lambda argument — are left
// to its own rendering.
func (p *printer) flatRegion(buf source.Buffer, region []cst.Tree, kind cst.Kind) {
	first := true
	for _, child := range region {
		if isListSeparator(child) {
			continue
		}
		if !first {
			p.leaf(token.Comma, kind, ",")
		}
		p.walk(buf, child, kind)
		first = false
	}
}

// expandRegion renders one element per line, each followed by a synthesized
// trailing comma. The newline before each element drives the structural indent;
// the source trivia and commas are dropped and regenerated.
func (p *printer) expandRegion(buf source.Buffer, region []cst.Tree, kind cst.Kind) {
	for _, child := range region {
		if isListSeparator(child) {
			continue
		}
		p.leaf(token.Newline, kind, "\n")
		p.walk(buf, child, kind)
		p.leaf(token.Comma, kind, ",")
	}
}

// isCommaList reports whether a node kind is a comma-separated list the
// magic-trailing-comma rule governs.
func isCommaList(k cst.Kind) bool {
	switch k {
	case cst.RecordLit, cst.RecordType, cst.CollectionLit, cst.CallExpr,
		cst.ParamList, cst.GenericArgs, cst.GenericParams, cst.UseList:
		return true
	}
	return false
}

// bracketBounds returns the indices of the list's opening and closing bracket
// among its children — the first opener and the last closer — or (-1, -1) when
// the list is not bracketed as expected (an unclosed list during recovery).
func bracketBounds(children []cst.Tree) (openIdx, closeIdx int) {
	openIdx, closeIdx = -1, -1
	for i, child := range children {
		ck, ok := child.TokenKind()
		if !ok {
			continue
		}
		if openIdx < 0 && isListOpen(ck) {
			openIdx = i
		}
		if isListClose(ck) {
			closeIdx = i
		}
	}
	return openIdx, closeIdx
}

// regionIsMultiline reports whether the author broke the list across lines: a
// newline sits directly between the brackets. A newline buried inside an element
// (a multi-line lambda) does not count — only the list's own breaks do.
func regionIsMultiline(region []cst.Tree) bool {
	for _, child := range region {
		if ck, ok := child.TokenKind(); ok && ck == token.Newline {
			return true
		}
	}
	return false
}

// regionHasComment reports whether a comment sits directly between the brackets,
// in which case the list is rendered as written to keep the comment placed.
func regionHasComment(region []cst.Tree) bool {
	for _, child := range region {
		if ck, ok := child.TokenKind(); ok && isComment(ck) {
			return true
		}
	}
	return false
}

// isListSeparator reports whether a child is list punctuation or trivia the
// renderer regenerates: a comma, a newline, or whitespace.
func isListSeparator(child cst.Tree) bool {
	ck, ok := child.TokenKind()
	return ok && (ck == token.Comma || ck == token.Newline || ck == token.Whitespace)
}

// isListOpen / isListClose recognize every list's brackets, angle brackets
// included (generic lists) — wider than the indenting isOpen/isClose, which
// leave "<"/">" out because generics do not nest indentation.
func isListOpen(k token.Kind) bool {
	return k == token.LBrace || k == token.LBracket || k == token.LParen || k == token.Lt
}

func isListClose(k token.Kind) bool {
	return k == token.RBrace || k == token.RBracket || k == token.RParen || k == token.Gt
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
