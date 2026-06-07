// Package treetext implements the line discipline shared by the source trees'
// text formats (F-4): one element per line, nesting expressed by two-space
// indentation, the line body free-form for the layer to interpret. The reader
// turns raw text into depth-annotated lines and rejects everything the formats
// never produce — tabs in the indentation, odd indents, blank interior lines —
// so each layer's unmarshaler starts from a well-formed line stream and only
// has to parse line bodies. The writer is the inverse: it indents and joins
// lines so every marshaler emits the same shape by construction.
package treetext

import (
	"bytes"
	"fmt"
	"strings"
)

// Indent is the indentation unit: the number of spaces per nesting level.
const Indent = 2

// Line is one element line of a tree text: its nesting depth and the line body
// past the indentation. Number is the 1-based line number in the input, kept so
// an unmarshaler can position its errors.
type Line struct {
	Depth   int
	Content string
	Number  int
}

// Lines splits data into depth-annotated element lines. It rejects input the
// writers never produce: indentation that uses tabs or is not a multiple of
// the indent unit, and blank lines anywhere but a trailing final newline.
func Lines(data []byte) ([]Line, error) {
	var out []Line
	rest := data
	for number := 1; len(rest) > 0; number++ {
		raw, after, _ := bytes.Cut(rest, []byte{'\n'})
		rest = after
		// A trailing carriage return is transport noise (git autocrlf, an
		// editor saving CRLF): the writers never emit one, so stripping it
		// keeps the round trip alive across line-ending normalization
		// without admitting \r anywhere meaningful.
		raw = bytes.TrimSuffix(raw, []byte{'\r'})
		line := string(raw)
		body := strings.TrimLeft(line, " ")
		if body == "" {
			if len(rest) == 0 && line == "" {
				break // the final trailing newline
			}
			return nil, fmt.Errorf("treetext: line %d: blank line", number)
		}
		if body[0] == '\t' {
			return nil, fmt.Errorf("treetext: line %d: tab in indentation", number)
		}
		indent := len(line) - len(body)
		if indent%Indent != 0 {
			return nil, fmt.Errorf("treetext: line %d: indentation of %d spaces is not a multiple of %d", number, indent, Indent)
		}
		out = append(out, Line{Depth: indent / Indent, Content: body, Number: number})
	}
	return out, nil
}

// Writer accumulates element lines in the shared shape: each Line call emits
// one line at the given depth, terminated by a newline.
type Writer struct {
	b bytes.Buffer
}

// Line appends one element line at the given depth.
func (w *Writer) Line(depth int, content string) {
	for range depth * Indent {
		w.b.WriteByte(' ')
	}
	w.b.WriteString(content)
	w.b.WriteByte('\n')
}

// Bytes returns the accumulated text.
func (w *Writer) Bytes() []byte {
	return w.b.Bytes()
}
