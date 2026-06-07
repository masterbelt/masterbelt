package concrete

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lexer"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Document is an incrementally maintained concrete syntax tree over an editable
// source.
//
// It layers on the lexer's incremental Document: after an edit the token stream
// is relexed only around the change, and Edit then reparses only the File-level
// declarations that the change actually touched, reusing the green subtrees of
// every declaration before and after it. Reuse is sound because the parser is
// context-free at File-child boundaries — a declaration's parse depends only on
// its own tokens — so the unedited declarations parse to exactly the same green
// nodes they did before. And because green nodes are width-based, splicing the
// reused and freshly parsed declarations back together is just concatenation:
// no offsets are rewritten.
//
// The tree and diagnostics are always identical to what a fresh parse of the
// current text would produce.
type Document struct {
	lex   *lexer.Document
	root  *cst.Node
	diags []diagnostic.Diagnostic
}

// NewDocument lexes and parses src, then keeps the result up to date across
// Edits.
func NewDocument(src []byte) *Document {
	lx := lexer.NewDocument(src)
	root, diags := parseTokens(lx.Tokens(), lx.Buffer())
	return &Document{lex: lx, root: root, diags: diags}
}

// Buffer returns the underlying editable buffer, for resolving the source text
// and spans of tree elements and diagnostics.
func (d *Document) Buffer() source.Buffer { return d.lex.Buffer() }

// Root returns the green File node at the root of the current tree.
func (d *Document) Root() *cst.Node { return d.root }

// Tree returns the positioned root of the current tree.
func (d *Document) Tree() cst.Tree { return cst.Root(d.root) }

// Diagnostics returns the current parse diagnostics, ordered by offset.
func (d *Document) Diagnostics() []diagnostic.Diagnostic { return d.diags }

// LexDiagnostics returns the current lexer-phase diagnostics, ordered by offset.
func (d *Document) LexDiagnostics() []diagnostic.Diagnostic { return d.lex.Diagnostics() }

// Edit applies e and incrementally updates the tree and parse diagnostics.
//
// The strategy mirrors the lexer's relexer, one level up: keep the declarations
// before the edit, reparse from the boundary of the first declaration the edit
// reaches, and as soon as a freshly parsed declaration realigns with an
// unchanged declaration boundary in the old tree, splice the old tail back on.
func (d *Document) Edit(e source.Edit) {
	oldChildren := d.root.Children()
	oldOffsets := childOffsets(oldChildren)
	oldTokens := d.lex.Tokens()
	oldDiags := d.diags
	delta := len(e.NewText) - (e.End - e.Start)

	// Relex first so the new token stream reflects the edit.
	d.lex.Edit(e)
	newTokens := d.lex.Tokens()
	newLen := d.lex.Buffer().Len()

	// Pick the declaration boundary to reparse from. It must sit left of the edit
	// far enough to absorb two effects:
	//
	//   - Token merges. An insertion can fuse the token at the edit with its
	//     neighbour (1 + 2 -> 12), dissolving the boundary at e.Start. The lexer
	//     would relex from the start of the first token reaching the edit, so we
	//     never start right of that point — a boundary the relexer preserves.
	//
	//   - Forward lookahead. A declaration's right edge can depend on the tokens
	//     that follow it: an Error node runs until the next declaration starter,
	//     and an incomplete declaration peeks for an optional ":"/"=". That
	//     lookahead skips trivia, so it can reach well past the immediately
	//     following child (an Error node at end of file scans across its trailing
	//     comments to EOF). It can even cross significant tokens: whether a run
	//     stops at fn ([pub] extern) hinges on the name (fn) that follows it,
	//     across an effect list (see beginsDeclaration). reparseStart re-derives
	//     every affected right edge by anchoring at/before the last significant
	//     token before the edit that no such lookahead can see past.
	winStart, iStart := reparseStart(oldTokens, oldOffsets, len(oldChildren), e.Start)
	prefix := oldChildren[:iStart]

	// Reparse forward from winStart in the new token stream.
	p := newParser(newTokens, d.lex.Buffer())
	p.pos = tokenIndexAt(newTokens, winStart)
	changedEnd := e.Start + len(e.NewText)

	var fresh []cst.Green
	freshEnd := winStart
	for {
		batch, done := p.nextChildren()
		if done {
			// Reached EOF without realigning: the whole tail was reparsed.
			fresh = append(fresh, batch...)
			d.commit(prefix, fresh, nil)
			d.diags = spliceDiags(oldDiags, p.diags.Items(), winStart, newLen-delta, delta)
			return
		}

		// A non-final batch is exactly one declaration/error node.
		fresh = append(fresh, batch...)
		w := batchWidth(batch)
		if w == 0 {
			// Progress guard — mirror parseFile: a zero-width batch must not
			// spin this loop. Take one token as a raw leaf so the reparse
			// advances; the full parse applies the same guard, so the trees
			// stay identical.
			leaf := p.bump()
			fresh = append(fresh, leaf)
			w = leaf.Width()
		}
		freshEnd += w

		// Realign only once we are past the changed region: then the bytes from
		// here on are unchanged, so a matching old boundary makes the old tail
		// reusable verbatim (shifted by delta).
		if freshEnd >= changedEnd {
			if q, ok := childIndexAt(oldOffsets, len(oldChildren), freshEnd-delta); ok {
				d.commit(prefix, fresh, oldChildren[q:])
				d.diags = spliceDiags(oldDiags, p.diags.Items(), winStart, freshEnd-delta, delta)
				return
			}
		}
	}
}

// commit replaces the root with prefix + fresh + suffix. No offsets are
// rewritten: green nodes are width-based, so concatenation alone yields a tree
// whose every position is correct.
func (d *Document) commit(prefix, fresh, suffix []cst.Green) {
	children := make([]cst.Green, 0, len(prefix)+len(fresh)+len(suffix))
	children = append(children, prefix...)
	children = append(children, fresh...)
	children = append(children, suffix...)
	d.root = cst.NewNode(cst.File, children)
}

// childOffsets returns the absolute start offset of each child plus a trailing
// sentinel equal to the total width (offsets[len(children)]).
func childOffsets(children []cst.Green) []int {
	offsets := make([]int, len(children)+1)
	for i, c := range children {
		offsets[i+1] = offsets[i] + c.Width()
	}
	return offsets
}

// childIndexAt returns the index of the child whose start offset equals target,
// searching only the n real children (not the trailing sentinel).
func childIndexAt(offsets []int, n, target int) (int, bool) {
	i := sort.Search(n, func(i int) bool { return offsets[i] >= target })
	if i < n && offsets[i] == target {
		return i, true
	}
	return 0, false
}

// childContaining returns the index of the child whose half-open span contains
// off (offsets[i] <= off < offsets[i+1]), clamped to the last child when off is
// at or past the end and to the first child when off is non-positive.
func childContaining(offsets []int, n, off int) int {
	if n == 0 || off <= 0 {
		return 0
	}
	i := sort.Search(n, func(i int) bool { return offsets[i+1] > off })
	if i >= n {
		return n - 1
	}
	return i
}

// lexSafePoint returns the offset the lexer would relex from for an edit at
// eStart: the start of the first token that reaches the edit, but never past
// eStart itself. A boundary at or before this point is preserved in the relexed
// stream, so it is a safe place to restart parsing even when the edit merges the
// tokens straddling it. Every token strictly left of it is unchanged.
func lexSafePoint(oldTokens []token.Token, eStart int) int {
	i := 0
	for i < len(oldTokens) && oldTokens[i].End() < eStart {
		i++
	}
	if i < len(oldTokens) && oldTokens[i].Offset < eStart {
		return oldTokens[i].Offset
	}
	return eStart
}

// reparseStart returns the child boundary to restart parsing from for an edit at
// eStart, and that child's index. It anchors on the last significant token that
// ends at or before the lexer's safe relex point — the furthest right a token
// can be while staying unchanged — and snaps to the start of the child holding
// it.
//
// The anchor additionally backs off past any trailing run of lookaheadChain
// tokens (pub/extern/fn and the effect keywords). A File-child boundary can
// hinge on a multi-token lookahead across exactly such a run — an error run
// stops at fn only when a name follows ([pub] extern only when fn follows), and
// fn skips an effect list to find its name — so a construct whose right edge
// was decided by looking across that run must be reparsed when the tokens after
// the run change. Once the anchor is a token no decision can see past, the
// declaration ending at that boundary parses identically in the new stream, so
// it (and everything before it) is reusable while the boundary stays valid.
func reparseStart(oldTokens []token.Token, oldOffsets []int, n, eStart int) (winStart, iStart int) {
	safe := lexSafePoint(oldTokens, eStart)

	last := -1
	for i, t := range oldTokens {
		if t.End() > safe {
			break
		}
		last = i
	}
	j := last
	for j >= 0 && (isTrivia(oldTokens[j].Kind) || lookaheadChain(oldTokens[j].Kind)) {
		j--
	}
	if j < 0 {
		return 0, 0
	}
	iStart = childContaining(oldOffsets, n, oldTokens[j].Offset)
	return oldOffsets[iStart], iStart
}

// lookaheadChain reports whether kind can sit inside a boundary decision's
// lookahead window: pub and extern are examined on the way to the fn of an
// extern-function declaration, fn looks past the effect keywords for its
// declaring name, and any of them can itself be the decision token. A child
// boundary anchored on such a token can change meaning when the tokens after it
// change, so reparseStart refuses to anchor on one.
func lookaheadChain(k token.Kind) bool {
	return k == token.Pub || k == token.Extern || k == token.Fn || k.Effect()
}

// tokenIndexAt returns the index of the token starting exactly at off. The
// caller only ever asks for a boundary in the unchanged region, which the
// relexer preserves, so a match always exists.
func tokenIndexAt(toks []token.Token, off int) int {
	return sort.Search(len(toks), func(i int) bool { return toks[i].Offset >= off })
}

// spliceDiags assembles the parse diagnostics the way the tree is assembled:
// the old ones from the reused prefix, then every freshly produced one, then the
// old ones from the reused tail shifted by delta. Every diagnostic is anchored
// strictly inside the declaration that produced it (see parser.lastStart), and
// winStart/resyncOld are declaration boundaries, so each old diagnostic falls
// cleanly into exactly one region — no boundary diagnostic is dropped or
// duplicated. The fresh diagnostics are the authoritative set for the reparsed
// window and already carry absolute offsets, so they are kept whole and unshifted.
func spliceDiags(old, fresh []diagnostic.Diagnostic, winStart, resyncOld, delta int) []diagnostic.Diagnostic {
	var res []diagnostic.Diagnostic
	for _, d := range old {
		if d.Offset < winStart {
			res = append(res, d)
		}
	}
	res = append(res, fresh...)
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
