package treetext

// This file is the element grammar of the exact tree formats (format v2,
// F-4 §2.2): every node is an element — a heading line carrying its type name
// — followed by one field line per struct field, in declaration order. A field
// line is "Name:" plus one of three tails:
//
//	Name: <scalars>      an inline scalar value: quoted strings, a bool, an
//	                     integer, or the nil marker "~"
//	Name: TypeName       a single child node, its fields one level deeper
//	Name:                a list of child elements, each one level deeper
//
// The three are distinguished without type knowledge: a tail starting with an
// uppercase letter is a type name (scalars start with a quote, a digit, a
// sign, or are the lowercase true/false/~), and an empty tail opens a list.
// A list item line is a type-name heading, or "~" for a nil element. The
// decoders generated over this grammar then enforce the expected shape per
// field, so the parser here stays generic.

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Nil is the inline marker for a nil node, a nil slice, or an absent value.
const Nil = "~"

// Element is one node of a tree text: its type-name heading and its fields.
type Element struct {
	Head   string
	Fields []Field
	Line   int // 1-based input line of the heading, for error positions
}

// Field is one field line of an element, in one of the three tail forms:
// Inline holds the scalar tail (including the Nil marker), Node the single
// child of the type-name form, and Items the elements of the list form.
type Field struct {
	Name   string
	Inline string
	Node   *Element
	Items  []Element
	Line   int // 1-based input line, for error positions
}

// Parse reads a tree text into its root element. The input must hold exactly
// one root.
func Parse(data []byte) (*Element, error) {
	lines, err := Lines(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("treetext: empty text")
	}
	p := &elementParser{lines: lines}
	root, err := p.parseElement(0)
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.lines) {
		return nil, fmt.Errorf("treetext: line %d: a second root element", p.lines[p.pos].Number)
	}
	return root, nil
}

// elementParser is a cursor over the depth-annotated lines.
type elementParser struct {
	lines []Line
	pos   int
}

// fieldLine splits a field line's content into its name and tail, reporting
// whether the line is a field line at all (its first token ends with a colon).
func fieldLine(content string) (name, tail string, ok bool) {
	head, rest, _ := strings.Cut(content, " ")
	if !strings.HasSuffix(head, ":") || len(head) == 1 {
		return "", "", false
	}
	return head[:len(head)-1], rest, true
}

// parseElement parses the element whose heading sits at the cursor, which must
// be at depth: the heading line, then its field lines one level deeper.
func (p *elementParser) parseElement(depth int) (*Element, error) {
	line := p.lines[p.pos]
	if line.Depth != depth {
		return nil, fmt.Errorf("treetext: line %d: depth %d element where depth %d was expected", line.Number, line.Depth, depth)
	}
	if _, _, isField := fieldLine(line.Content); isField || strings.Contains(line.Content, " ") {
		return nil, fmt.Errorf("treetext: line %d: %q is not an element heading", line.Number, line.Content)
	}
	e := &Element{Head: line.Content, Line: line.Number}
	p.pos++
	for p.pos < len(p.lines) && p.lines[p.pos].Depth > depth {
		f, err := p.parseField(depth + 1)
		if err != nil {
			return nil, err
		}
		e.Fields = append(e.Fields, *f)
	}
	return e, nil
}

// parseField parses the field line at the cursor, which must sit at depth,
// along with the child lines its tail form owns.
func (p *elementParser) parseField(depth int) (*Field, error) {
	line := p.lines[p.pos]
	if line.Depth != depth {
		return nil, fmt.Errorf("treetext: line %d: depth %d line where a depth %d field was expected", line.Number, line.Depth, depth)
	}
	name, tail, ok := fieldLine(line.Content)
	if !ok {
		return nil, fmt.Errorf("treetext: line %d: %q is not a field line", line.Number, line.Content)
	}
	f := &Field{Name: name, Line: line.Number}
	p.pos++
	switch {
	case tail == "":
		// The list form: child elements one level deeper, possibly none.
		for p.pos < len(p.lines) && p.lines[p.pos].Depth > depth {
			item, err := p.parseItem(depth + 1)
			if err != nil {
				return nil, err
			}
			f.Items = append(f.Items, *item)
		}
		if f.Items == nil {
			return nil, fmt.Errorf("treetext: line %d: field %s: a list form with no items (an empty list is %q)", line.Number, name, Nil)
		}
	case isHead(tail):
		// The single-node form: the tail is the child's heading, its fields
		// one level deeper.
		child := &Element{Head: tail, Line: line.Number}
		for p.pos < len(p.lines) && p.lines[p.pos].Depth > depth {
			cf, err := p.parseField(depth + 1)
			if err != nil {
				return nil, err
			}
			child.Fields = append(child.Fields, *cf)
		}
		f.Node = child
	default:
		// The scalar form owns no children.
		if p.pos < len(p.lines) && p.lines[p.pos].Depth > depth {
			return nil, fmt.Errorf("treetext: line %d: children under the scalar field %s", p.lines[p.pos].Number, name)
		}
		f.Inline = tail
	}
	return f, nil
}

// parseItem parses one list item at depth: a child element, or the Nil marker
// for a nil entry.
func (p *elementParser) parseItem(depth int) (*Element, error) {
	line := p.lines[p.pos]
	if line.Depth == depth && line.Content == Nil {
		p.pos++
		return &Element{Head: Nil, Line: line.Number}, nil
	}
	return p.parseElement(depth)
}

// isHead reports whether a field tail is a type-name heading (the single-node
// form) rather than a scalar: type names are exported Go identifiers, so they
// start with an uppercase letter, which no scalar does.
func isHead(tail string) bool {
	return tail[0] >= 'A' && tail[0] <= 'Z' && !strings.Contains(tail, " ")
}

// --- the scalar decoders the generated codecs share -------------------------

// String decodes a field's tail as one quoted string.
func String(f Field) (string, error) {
	if f.Node != nil || f.Items != nil {
		return "", fmt.Errorf("treetext: line %d: field %s: expected a string", f.Line, f.Name)
	}
	v, err := strconv.Unquote(f.Inline)
	if err != nil {
		return "", fmt.Errorf("treetext: line %d: field %s: malformed string %s", f.Line, f.Name, f.Inline)
	}
	return v, nil
}

// Strings decodes a field's tail as a space-separated run of quoted strings,
// with the Nil marker for none.
func Strings(f Field) ([]string, error) {
	if f.Node != nil || f.Items != nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: expected strings", f.Line, f.Name)
	}
	if f.Inline == Nil {
		return nil, nil
	}
	var out []string
	rest := f.Inline
	for rest != "" {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil || q[0] != '"' {
			return nil, fmt.Errorf("treetext: line %d: field %s: malformed strings at %q", f.Line, f.Name, rest)
		}
		v, err := strconv.Unquote(q)
		if err != nil {
			return nil, fmt.Errorf("treetext: line %d: field %s: malformed string %s", f.Line, f.Name, q)
		}
		out = append(out, v)
		rest = rest[len(q):]
		if rest != "" {
			var found bool
			rest, found = strings.CutPrefix(rest, " ")
			if !found {
				return nil, fmt.Errorf("treetext: line %d: field %s: strings must be space-separated", f.Line, f.Name)
			}
		}
	}
	if out == nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: empty strings tail (none is %q)", f.Line, f.Name, Nil)
	}
	return out, nil
}

// Bool decodes a field's tail as true or false.
func Bool(f Field) (bool, error) {
	if f.Node == nil && f.Items == nil {
		switch f.Inline {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("treetext: line %d: field %s: expected true or false", f.Line, f.Name)
}

// Int decodes a field's tail as a decimal integer.
func Int(f Field) (int, error) {
	if f.Node != nil || f.Items != nil {
		return 0, fmt.Errorf("treetext: line %d: field %s: expected an integer", f.Line, f.Name)
	}
	v, err := strconv.Atoi(f.Inline)
	if err != nil {
		return 0, fmt.Errorf("treetext: line %d: field %s: malformed integer %q", f.Line, f.Name, f.Inline)
	}
	return v, nil
}

// Int64 decodes a field's tail as a decimal 64-bit integer.
func Int64(f Field) (int64, error) {
	if f.Node != nil || f.Items != nil {
		return 0, fmt.Errorf("treetext: line %d: field %s: expected an integer", f.Line, f.Name)
	}
	v, err := strconv.ParseInt(f.Inline, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("treetext: line %d: field %s: malformed integer %q", f.Line, f.Name, f.Inline)
	}
	return v, nil
}

// BigInt decodes a field's tail as an arbitrary-precision decimal integer,
// with the Nil marker for none.
func BigInt(f Field) (*big.Int, error) {
	if f.Node != nil || f.Items != nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: expected an integer", f.Line, f.Name)
	}
	if f.Inline == Nil {
		return nil, nil
	}
	v, ok := new(big.Int).SetString(f.Inline, 10)
	if !ok {
		return nil, fmt.Errorf("treetext: line %d: field %s: malformed integer %q", f.Line, f.Name, f.Inline)
	}
	return v, nil
}

// ExpectFields checks that an element carries exactly the named fields in
// order — the canonical shape the writers emit, which is the only shape the
// decoders accept.
func ExpectFields(e *Element, names ...string) error {
	if len(e.Fields) != len(names) {
		return fmt.Errorf("treetext: line %d: %s has %d fields, want %d", e.Line, e.Head, len(e.Fields), len(names))
	}
	for i, want := range names {
		if e.Fields[i].Name != want {
			return fmt.Errorf("treetext: line %d: %s field %d is %s, want %s", e.Line, e.Head, i, e.Fields[i].Name, want)
		}
	}
	return nil
}

// QuoteStrings renders a string slice as the inline tail Strings decodes: the
// space-separated quoted values, or the Nil marker for none.
func QuoteStrings(vs []string) string {
	if len(vs) == 0 {
		return Nil
	}
	quoted := make([]string, len(vs))
	for i, v := range vs {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, " ")
}
