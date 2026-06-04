package lexer

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// Document is an incrementally maintained token stream and diagnostic set over
// an editable Text.
//
// It exists to back an incremental parser: after an edit, re-tokenizing the
// whole file is wasteful, so Edit re-lexes only a bounded window around the
// change and reuses the unaffected tokens (and diagnostics) on either side.
// This works because the lexer is context-free at token boundaries — a token's
// identity depends only on the bytes it covers, not on any carried lexer state —
// so re-lexing can start at any token boundary and stop as soon as it realigns
// with the old stream. Because tokens and diagnostics are both offset-based,
// reuse is just an offset shift.
//
// The token stream and diagnostics are always identical to what a fresh lex of
// the current text would produce.
type Document struct {
	text   *source.Text
	tokens []token.Token
	diags  []diagnostic.Diagnostic
}

// NewDocument creates a Document over a copy-free view of src and lexes it once.
func NewDocument(src []byte) *Document {
	tokens, diags := lex(src)
	return &Document{
		text:   source.NewText(src),
		tokens: append(tokens, token.Token{Kind: token.EOF, Offset: len(src), Width: 0}),
		diags:  diags,
	}
}

// Buffer returns the underlying editable buffer, for resolving token and
// diagnostic spans (tok.Span(doc.Buffer()), diag.Span(doc.Buffer())).
func (d *Document) Buffer() source.Buffer {
	return d.text
}

// Tokens returns the current token stream, terminated by an EOF token.
func (d *Document) Tokens() []token.Token {
	return d.tokens
}

// Diagnostics returns the current diagnostics, ordered by offset.
func (d *Document) Diagnostics() []diagnostic.Diagnostic {
	return d.diags
}

// relexMargin is how far past the edited region the first relex window reaches,
// in bytes, before the window starts doubling.
const relexMargin = 32

// Edit applies e to the document and incrementally updates the token stream and
// diagnostics.
func (d *Document) Edit(e source.Edit) {
	oldTokens, oldDiags := d.tokens, d.diags
	delta := len(e.NewText) - (e.End - e.Start)

	d.text.Edit(e.Start, e.End, e.NewText)
	newLen := d.text.Len()

	// Re-lex from the first token that reaches the edit — the one containing or
	// immediately left of e.Start. Earlier tokens cannot be affected: this
	// lexer has no lookbehind, and the only token that scans arbitrarily far
	// ahead is the block comment, whose span (to its */ or to EOF) makes its
	// End reach e.Start whenever the edit could change it. The terminating EOF
	// always satisfies End() >= e.Start, so oldTokens[iStart] is always valid.
	iStart := 0
	for iStart < len(oldTokens) && oldTokens[iStart].End() < e.Start {
		iStart++
	}
	winStart := min(e.Start, oldTokens[iStart].Offset)
	prefix := oldTokens[:iStart]

	// changedEnd is the end of the edited region in the new coordinates; the
	// reusable suffix can only begin at or after it.
	changedEnd := e.Start + len(e.NewText)
	winEnd := min(changedEnd+relexMargin, newLen)

	for {
		freshTokens, freshDiags := lex(d.text.Slice(winStart, winEnd))
		atEnd := winEnd >= newLen

		if fi, oi, ok := findResync(freshTokens, oldTokens, winStart, winEnd, delta, changedEnd, atEnd); ok {
			resyncNew := winStart + freshTokens[fi].Offset
			resyncOld := oldTokens[oi].Offset
			d.tokens = spliceTokens(prefix, freshTokens[:fi], oldTokens[oi:], winStart, delta)
			d.diags = spliceDiags(oldDiags, freshDiags, winStart, resyncNew, resyncOld, delta)
			return
		}
		if atEnd {
			// No reusable suffix: keep the prefixes and the freshly lexed tail.
			d.tokens = appendEOF(spliceTokens(prefix, freshTokens, nil, winStart, delta), newLen)
			d.diags = spliceDiags(oldDiags, freshDiags, winStart, newLen, newLen-delta, delta)
			return
		}
		winEnd = min(newLen, winStart+max(2*relexMargin, 2*(winEnd-winStart)))
	}
}

// findResync looks for the first complete fresh token (past the changed region)
// that realigns with an unchanged old token at the shifted offset. It returns
// the fresh and old indices of that token; ok is false when the window must
// grow to find one.
func findResync(fresh, old []token.Token, winStart, winEnd, delta, changedEnd int, atEnd bool) (int, int, bool) {
	for fi := range fresh {
		ft := fresh[fi]
		absStart := winStart + ft.Offset
		if absStart < changedEnd {
			continue // still inside the changed region
		}
		// A token touching the window's edge may be truncated; trust it only
		// when we lexed to the true end of the document.
		if !atEnd && absStart+ft.Width >= winEnd {
			break
		}
		// absStart >= changedEnd guarantees the match is in the unchanged suffix.
		oi := findToken(old, absStart-delta)
		if oi < 0 || old[oi].Kind != ft.Kind || old[oi].Width != ft.Width {
			continue
		}
		return fi, oi, true
	}
	return 0, 0, false
}

// spliceTokens assembles prefix + the window-relative freshMiddle (shifted to
// absolute) + the old suffix (shifted by delta).
func spliceTokens(prefix, freshMiddle, oldSuffix []token.Token, winStart, delta int) []token.Token {
	res := make([]token.Token, 0, len(prefix)+len(freshMiddle)+len(oldSuffix))
	res = append(res, prefix...)
	for _, t := range freshMiddle {
		res = append(res, token.Token{Kind: t.Kind, Offset: winStart + t.Offset, Width: t.Width})
	}
	for _, t := range oldSuffix {
		res = append(res, token.Token{Kind: t.Kind, Offset: t.Offset + delta, Width: t.Width})
	}
	return res
}

func appendEOF(tokens []token.Token, offset int) []token.Token {
	return append(tokens, token.Token{Kind: token.EOF, Offset: offset, Width: 0})
}

// spliceDiags assembles the diagnostics the same way as tokens: old diagnostics
// before the window, the freshly lexed ones inside it (up to the realignment
// point), and the old suffix shifted by delta. The three ranges are disjoint and
// offset-ordered, matching a full lex.
func spliceDiags(old, fresh []diagnostic.Diagnostic, winStart, resyncNew, resyncOld, delta int) []diagnostic.Diagnostic {
	var res []diagnostic.Diagnostic
	for _, d := range old {
		if d.End() <= winStart {
			res = append(res, d)
		}
	}
	for _, d := range fresh {
		if winStart+d.Offset < resyncNew {
			res = append(res, shiftDiag(d, winStart))
		}
	}
	for _, d := range old {
		if d.Offset >= resyncOld {
			res = append(res, shiftDiag(d, delta))
		}
	}
	return res
}

func shiftDiag(d diagnostic.Diagnostic, by int) diagnostic.Diagnostic {
	d.Offset += by
	return d
}

// findToken returns the index of the token starting exactly at off, or -1.
func findToken(tokens []token.Token, off int) int {
	i := sort.Search(len(tokens), func(i int) bool { return tokens[i].Offset >= off })
	if i < len(tokens) && tokens[i].Offset == off {
		return i
	}
	return -1
}
