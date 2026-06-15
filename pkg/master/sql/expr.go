package sql

import "strings"

// sqlExpr is the small SQL expression AST the lowering builds before rendering —
// kept apart from the string so the rendering is one deterministic place (column
// quoting, operator spelling, parenthesization) that every dialect shares.
type sqlExpr interface{ sqlExpr() }

type column struct{ name string }
type placeholder struct{}
type binary struct {
	op   string
	l, r sqlExpr
}
type unary struct{ x sqlExpr } // NOT x
type nullTest struct {         // x IS [NOT] NULL
	x   sqlExpr
	not bool
}

func (column) sqlExpr()      {}
func (placeholder) sqlExpr() {}
func (binary) sqlExpr()      {}
func (unary) sqlExpr()       {}
func (nullTest) sqlExpr()    {}

// render writes a SQL expression deterministically for a dialect: a column is
// quoted the dialect's way, a placeholder is the dialect's n-th, and every
// compound is fully parenthesized so precedence is explicit and the output is
// stable for golden tests.
func render(e sqlExpr, d Dialect) string {
	var b strings.Builder
	n := 0
	write(&b, e, d, &n)
	return b.String()
}

func write(b *strings.Builder, e sqlExpr, d Dialect, n *int) {
	switch x := e.(type) {
	case column:
		b.WriteString(d.QuoteIdent(x.name))
	case placeholder:
		*n++
		b.WriteString(d.Placeholder(*n))
	case binary:
		b.WriteByte('(')
		write(b, x.l, d, n)
		b.WriteString(" " + x.op + " ")
		write(b, x.r, d, n)
		b.WriteByte(')')
	case unary:
		b.WriteString("(NOT ")
		write(b, x.x, d, n)
		b.WriteByte(')')
	case nullTest:
		b.WriteByte('(')
		write(b, x.x, d, n)
		if x.not {
			b.WriteString(" IS NOT NULL)")
		} else {
			b.WriteString(" IS NULL)")
		}
	}
}
