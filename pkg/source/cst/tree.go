package cst

import (
	"fmt"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Tree is a positioned view of a Green element: the element paired with its
// absolute byte offset within the file. Because greens are width-based, a Tree
// is computed lazily — Root anchors the green root at offset 0, and Children
// derives each child's offset by accumulating widths. This is the "red" layer
// that overlays absolute positions on the shared, position-independent greens.
type Tree struct {
	green  Green
	offset int
}

// Root anchors a green root at offset 0.
func Root(green Green) Tree {
	return Tree{green: green, offset: 0}
}

// Green returns the underlying green element.
func (t Tree) Green() Green { return t.green }

// Offset returns the element's absolute start offset.
func (t Tree) Offset() int { return t.offset }

// Width returns the element's byte length.
func (t Tree) Width() int { return t.green.Width() }

// End returns the byte offset one past the element (Offset + Width).
func (t Tree) End() int { return t.offset + t.green.Width() }

// Node returns the underlying *Node and true when this is an internal node.
func (t Tree) Node() (*Node, bool) {
	n, ok := t.green.(*Node)
	return n, ok
}

// Token returns the underlying *Token and true when this is a leaf token.
func (t Tree) Token() (*Token, bool) {
	tok, ok := t.green.(*Token)
	return tok, ok
}

// Kind returns the node kind, or false for a leaf token.
func (t Tree) Kind() (Kind, bool) {
	if n, ok := t.green.(*Node); ok {
		return n.kind, true
	}
	return 0, false
}

// TokenKind returns the token kind, or false for an internal node.
func (t Tree) TokenKind() (token.Kind, bool) {
	if tok, ok := t.green.(*Token); ok {
		return tok.kind, true
	}
	return 0, false
}

// Children returns the positioned children in source order, each carrying its
// own absolute offset. A leaf token has no children and yields nil.
func (t Tree) Children() []Tree {
	n, ok := t.green.(*Node)
	if !ok {
		return nil
	}
	out := make([]Tree, len(n.children))
	offset := t.offset
	for i, c := range n.children {
		out[i] = Tree{green: c, offset: offset}
		offset += c.Width()
	}
	return out
}

// Text returns the source text the element covers, read from buf.
func (t Tree) Text(buf source.Buffer) string {
	return string(buf.Slice(t.offset, t.End()))
}

// Span resolves the element's source span from buf on demand.
func (t Tree) Span(buf source.Buffer) source.Span {
	return buf.Span(t.offset, t.End())
}

// Sprint renders root as a stable, diffable tree snapshot: one element per line,
// indented by depth, with each node showing its Kind and each leaf showing its
// token Kind and quoted text. Concatenating the quoted leaf texts top to bottom
// reproduces the source, which is what makes the snapshot a losslessness check.
func Sprint(buf source.Buffer, root Green) string {
	var b strings.Builder
	writeTree(&b, buf, Root(root), 0)
	return b.String()
}

func writeTree(b *strings.Builder, buf source.Buffer, t Tree, depth int) {
	indent := strings.Repeat("  ", depth)
	if tok, ok := t.Token(); ok {
		fmt.Fprintf(b, "%s%s %q\n", indent, tok.Kind(), t.Text(buf))
		return
	}
	n, _ := t.Node()
	fmt.Fprintf(b, "%s%s\n", indent, n.Kind())
	for _, child := range t.Children() {
		writeTree(b, buf, child, depth+1)
	}
}

// Equal reports whether two green trees are structurally identical — same kinds,
// same widths, same children in order. It compares only the position-independent
// shape, so it is the oracle an incremental reparse is checked against: the
// spliced tree must equal a tree parsed from scratch.
func Equal(a, b Green) bool {
	switch x := a.(type) {
	case *Token:
		y, ok := b.(*Token)
		return ok && x.kind == y.kind && x.width == y.width
	case *Node:
		y, ok := b.(*Node)
		if !ok || x.kind != y.kind || x.width != y.width || len(x.children) != len(y.children) {
			return false
		}
		for i := range x.children {
			if !Equal(x.children[i], y.children[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
