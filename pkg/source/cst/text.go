// This file is the CST's text representation contract (F-4): every green
// element marshals to the one-element-per-line indented form the snapshots
// have always used, and a File round-trips back through UnmarshalText. The
// format was already exact — every token, every kind, trivia included — so it
// is adopted unchanged as the contract:
//
//	File
//	  ConstDecl
//	    Const "const"
//	    Whitespace " "
//	    ...
//
// A node line is its Kind name alone; a token line is its token.Kind name and
// the quoted source text. Concatenating the unquoted leaf texts in order
// reproduces the source byte for byte (Source), which is the losslessness
// property as a function of the tree — no buffer involved, so an unmarshaled
// tree is as complete as a parsed one.

package cst

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/internal/treetext"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// MarshalText renders the node and its subtree in the text representation.
func (n *Node) MarshalText() ([]byte, error) {
	var w treetext.Writer
	writeText(&w, n, 0)
	return w.Bytes(), nil
}

// MarshalText renders the leaf token in the text representation: its kind and
// quoted text on one line.
func (t *Token) MarshalText() ([]byte, error) {
	var w treetext.Writer
	writeText(&w, t, 0)
	return w.Bytes(), nil
}

// writeText emits one element line at depth and recurses over node children.
func writeText(w *treetext.Writer, g Green, depth int) {
	switch e := g.(type) {
	case *Token:
		w.Line(depth, e.kind.String()+" "+strconv.Quote(e.text))
	case *Node:
		w.Line(depth, e.kind.String())
		for _, c := range e.children {
			writeText(w, c, depth+1)
		}
	}
}

// UnmarshalText parses the text representation back into the node. The input
// must hold exactly one root element, and it must be a node — token texts are
// quoted, so a line with a quoted tail reads as a leaf and a bare kind name as
// a node. The result is detached by construction: greens never carried
// positions, so nothing is lost — wrapping the node in a Tree positions it
// exactly like a freshly parsed one.
func (n *Node) UnmarshalText(data []byte) error {
	lines, err := treetext.Lines(data)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return errors.New("cst: empty text")
	}
	p := &textParser{lines: lines}
	root, err := p.element(0)
	if err != nil {
		return err
	}
	if p.pos != len(p.lines) {
		return fmt.Errorf("cst: line %d: a second root element", p.lines[p.pos].Number)
	}
	rootNode, ok := root.(*Node)
	if !ok {
		return errors.New("cst: the root element is a token, want a node")
	}
	*n = *rootNode
	return nil
}

// textParser is a cursor over the element lines, building greens by descent:
// an element's children are the following lines exactly one level deeper.
type textParser struct {
	lines []treetext.Line
	pos   int
}

// element parses the element at the cursor, which must sit at depth.
func (p *textParser) element(depth int) (Green, error) {
	line := p.lines[p.pos]
	if line.Depth != depth {
		return nil, fmt.Errorf("cst: line %d: depth %d element where depth %d was expected", line.Number, line.Depth, depth)
	}
	head, rest, hasRest := strings.Cut(line.Content, " ")
	p.pos++
	if hasRest {
		text, err := strconv.Unquote(rest)
		if err != nil {
			return nil, fmt.Errorf("cst: line %d: malformed token text %s", line.Number, rest)
		}
		kind, ok := token.ParseKind(head)
		if !ok {
			return nil, fmt.Errorf("cst: line %d: unknown token kind %q", line.Number, head)
		}
		return NewToken(kind, text), nil
	}
	kind, ok := ParseKind(head)
	if !ok {
		return nil, fmt.Errorf("cst: line %d: unknown node kind %q", line.Number, head)
	}
	var children []Green
	for p.pos < len(p.lines) && p.lines[p.pos].Depth > depth {
		child, err := p.element(depth + 1)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return NewNode(kind, children), nil
}

// kindByName is the reverse of the Kind String table, built once on demand.
var kindByName = func() map[string]Kind {
	m := make(map[string]Kind, numKinds)
	for k := range numKinds {
		m[k.String()] = k
	}
	return m
}()

// ParseKind returns the Kind named name — the inverse of Kind.String — and
// whether the name is a known kind.
func ParseKind(name string) (Kind, bool) {
	k, ok := kindByName[name]
	return k, ok
}

// Source reconstructs the source text the element covers by concatenating its
// leaf texts in order — the tree's losslessness as a function, available on
// detached trees that never had a buffer.
func Source(root Green) []byte {
	var b strings.Builder
	b.Grow(root.Width())
	appendSource(&b, root)
	return []byte(b.String())
}

func appendSource(b *strings.Builder, g Green) {
	switch e := g.(type) {
	case *Token:
		b.WriteString(e.text)
	case *Node:
		for _, c := range e.children {
			appendSource(b, c)
		}
	}
}
