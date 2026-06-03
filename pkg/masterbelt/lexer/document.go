package lexer

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// Document is an incrementally maintained token stream over an editable Text.
//
// It exists to back an incremental parser: after an edit, re-tokenizing the
// whole file is wasteful, so Edit re-lexes only a bounded window around the
// change and reuses the unaffected tokens on either side. This works because
// the lexer is context-free at token boundaries — a token's identity depends
// only on the bytes it covers, not on any carried lexer state — so re-lexing
// can start at any token boundary and stop as soon as it realigns with the old
// stream.
//
// The token stream is always identical to what a fresh lex of the current text
// would produce. Diagnostics are not maintained incrementally; obtain them with
// a one-shot Lexer over Buffer when needed.
type Document struct {
	text   *source.Text
	tokens []token.Token
}

// NewDocument creates a Document over a copy-free view of src and lexes it once.
func NewDocument(src []byte) *Document {
	return &Document{
		text:   source.NewText(src),
		tokens: lexAll(src),
	}
}

// Buffer returns the underlying editable buffer, for resolving token text and
// spans (tok.Text(doc.Buffer()), tok.Span(doc.Buffer())).
func (d *Document) Buffer() source.Buffer {
	return d.text
}

// Tokens returns the current token stream, terminated by an EOF token.
func (d *Document) Tokens() []token.Token {
	return d.tokens
}

// lexAll lexes src in full and appends the terminating EOF token.
func lexAll(src []byte) []token.Token {
	return append(lexTokens(src), token.Token{Kind: token.EOF, Offset: len(src), Width: 0})
}

// Edit applies e to the document and incrementally updates the token stream.
func (d *Document) Edit(e source.Edit) {
	old := d.tokens
	delta := len(e.NewText) - (e.End - e.Start)

	d.text.Edit(e.Start, e.End, e.NewText)
	newLen := d.text.Len()

	// Re-lex from the first token that reaches the edit — the one containing or
	// immediately left of e.Start. Earlier tokens cannot be affected: this
	// lexer has no lookbehind, and the only token that scans arbitrarily far
	// ahead is the block comment, whose span (to its */ or to EOF) makes its
	// End reach e.Start whenever the edit could change it. The terminating EOF
	// always satisfies End() >= e.Start, so old[iStart] is always valid.
	iStart := 0
	for iStart < len(old) && old[iStart].End() < e.Start {
		iStart++
	}
	winStart := min(e.Start, old[iStart].Offset)
	prefix := old[:iStart]

	// changedEnd is the end of the edited region in the new coordinates; the
	// reusable suffix can only begin at or after it.
	changedEnd := e.Start + len(e.NewText)
	winEnd := min(changedEnd+relexMargin, newLen)

	for {
		fresh := lexTokens(d.text.Slice(winStart, winEnd))
		atEnd := winEnd >= newLen

		if spliced, ok := splice(prefix, fresh, old, winStart, winEnd, delta, changedEnd, atEnd); ok {
			d.tokens = spliced
			return
		}
		if atEnd {
			// No reusable suffix: keep the prefix and the freshly lexed tail.
			res := make([]token.Token, 0, len(prefix)+len(fresh)+1)
			res = append(res, prefix...)
			for _, ft := range fresh {
				res = append(res, shift(ft, winStart))
			}
			res = append(res, token.Token{Kind: token.EOF, Offset: newLen, Width: 0})
			d.tokens = res
			return
		}
		winEnd = min(newLen, winStart+max(2*relexMargin, 2*(winEnd-winStart)))
	}
}

// relexMargin is how far past the edited region the first relex window reaches,
// in bytes, before the window starts doubling.
const relexMargin = 32

// splice tries to realign the freshly lexed window with the old token stream.
// On success it returns prefix + freshly lexed middle + the old suffix shifted
// by delta. It fails (false) when no complete fresh token inside the window
// aligns with an unchanged old token, signalling that the window must grow.
func splice(prefix, fresh, old []token.Token, winStart, winEnd, delta, changedEnd int, atEnd bool) ([]token.Token, bool) {
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

		// absStart >= changedEnd guarantees oldStart >= e.End, so any match here
		// is necessarily in the unchanged suffix.
		oldStart := absStart - delta
		oi := findToken(old, oldStart)
		if oi < 0 || old[oi].Kind != ft.Kind || old[oi].Width != ft.Width {
			continue
		}

		// Realigned: identical token at the shifted offset means every
		// subsequent token matches too, since the suffix bytes are unchanged.
		res := make([]token.Token, 0, len(prefix)+fi+len(old)-oi)
		res = append(res, prefix...)
		for _, mt := range fresh[:fi] {
			res = append(res, shift(mt, winStart))
		}
		for _, st := range old[oi:] {
			res = append(res, token.Token{Kind: st.Kind, Offset: st.Offset + delta, Width: st.Width})
		}
		return res, true
	}
	return nil, false
}

// shift relocates a window-relative token to its absolute document offset.
func shift(t token.Token, base int) token.Token {
	return token.Token{Kind: t.Kind, Offset: base + t.Offset, Width: t.Width}
}

// findToken returns the index of the token starting exactly at off, or -1.
func findToken(tokens []token.Token, off int) int {
	i := sort.Search(len(tokens), func(i int) bool { return tokens[i].Offset >= off })
	if i < len(tokens) && tokens[i].Offset == off {
		return i
	}
	return -1
}
