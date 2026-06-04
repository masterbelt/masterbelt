// Package assert renders a failed compile-time assertion as a power-assert
// diagram: the condition in canonical surface syntax with the evaluated value
// of every sub-expression drawn beneath the place it appears.
//
//	MaxLevel < MinLevel
//	|        | |
//	100      | 0
//	         false
//
// The condition line and the anchor columns come from ast.RenderTrace, which
// inverts the operator desugaring while recording where each sub-expression's
// value reads best (an operator form at its operator, a reference at its
// name). The values come from the same constant folding the assertion was
// checked with, read through an eval.Env so the semantic engine's memoization
// and dependency tracking apply here too.
package assert

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Diagram renders the power-assert diagram for cond: the rendered condition
// followed by a pipe row and the value rows. A sub-expression that does not
// fold (or folds to a function value) contributes no row; with no foldable
// sub-expressions at all the diagram is just the condition line.
func Diagram(cond ast.Expr, env eval.Env) string {
	text, anchors := ast.RenderTrace(cond)

	markers := make([]marker, 0, len(anchors))
	seen := map[int]bool{}
	for _, a := range anchors {
		v := eval.Expr(a.Expr, env)
		if v == nil || v.Kind == ir.ConstFunc || seen[a.Col] {
			continue
		}
		seen[a.Col] = true
		markers = append(markers, marker{col: a.Col, val: v.String()})
	}
	if len(markers) == 0 {
		return text
	}
	sort.Slice(markers, func(i, j int) bool { return markers[i].col < markers[j].col })

	lines := append([]string{text}, layout(markers)...)
	return strings.Join(lines, "\n")
}

// marker is one value to draw: the rune column its pipe sits at and the
// rendered value.
type marker struct {
	col int
	val string
}

// layout arranges the markers below the condition: first a row of pipes at
// every column, then value rows packed greedily from the right — a value is
// printed on the earliest row where it fits before the next marker's column,
// and keeps a pipe on the rows above, so values never collide:
//
//	|   |   | |
//	100 | 0   false
//	    100
func layout(markers []marker) []string {
	row := newRow()
	for _, m := range markers {
		row.put(m.col, "|")
	}
	rows := []string{row.String()}

	pending := markers
	for len(pending) > 0 {
		row = newRow()
		var deferred []marker
		limit := int(^uint(0) >> 1) // the column the previous (righter) marker starts at
		for i := len(pending) - 1; i >= 0; i-- {
			m := pending[i]
			if m.col+utf8.RuneCountInString(m.val) <= limit {
				row.put(m.col, m.val)
			} else {
				row.put(m.col, "|")
				deferred = append([]marker{m}, deferred...)
			}
			limit = m.col
		}
		rows = append(rows, row.String())
		pending = deferred
	}
	return rows
}

// row is a line under construction, addressed by rune column.
type row struct{ runes []rune }

func newRow() *row { return &row{} }

// put writes s at rune column col, padding with spaces as needed.
func (r *row) put(col int, s string) {
	for len(r.runes) < col {
		r.runes = append(r.runes, ' ')
	}
	for i, c := range []rune(s) {
		if col+i < len(r.runes) {
			r.runes[col+i] = c
		} else {
			r.runes = append(r.runes, c)
		}
	}
}

func (r *row) String() string { return strings.TrimRight(string(r.runes), " ") }
