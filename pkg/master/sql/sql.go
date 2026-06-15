// Package sql lowers a row predicate — a belt boolean expression over a master's
// row (self) — to a single-table SQL boolean expression. It is the shared
// DSL→SQL primitive the validation engine, scope, and the sqlite-backed code
// generators all build on, so it is kept to one form and not forked.
//
// The lowering is a pure function over the resolved value graph (ir.Value): it
// imports only the IR it reads, never a SQL driver, so the generated SQL is a
// string with positional bind values and is executed elsewhere. A node it does
// not lower — an arithmetic operator, a column method, a row method, anything
// outside the comparison/logical/column/literal core — is returned as an
// Unsupported, for the caller to report against its own source provenance rather
// than be silently dropped.
package sql

import (
	"math/big"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Lowered is a predicate lowered to SQL: the boolean expression text, with each
// literal replaced by a positional ? placeholder, and the bind values in the
// order the placeholders appear. It is a fragment — the SELECT, the WHERE/NOT
// wrapper a validation needs, and the ORDER/LIMIT a scope adds are the caller's.
type Lowered struct {
	SQL   string
	Binds []Bind
}

// BindKind is the type of a bind value: the three literal kinds the core
// supports.
type BindKind int

const (
	// BindInt is an integer bind, its value in Int.
	BindInt BindKind = iota
	// BindText is a string bind, its value in Text.
	BindText
	// BindBool is a boolean bind, its value in Bool (rendered 0/1 by the caller
	// — SQLite has no boolean type).
	BindBool
)

// Bind is one positional bind value carried out of band from the SQL text.
type Bind struct {
	Kind BindKind
	Int  *big.Int
	Text string
	Bool bool
}

// Unsupported is a node the lowering could not express in the core SQL subset.
// Node is the offending value (it carries the syntax the caller anchors a
// diagnostic to); Reason names the construct, for the message.
type Unsupported struct {
	Node   ir.Value
	Reason string
}

// Lower lowers a row predicate to a SQL boolean expression and its bind values.
// A predicate the core cannot express yields one or more Unsupported entries and
// an SQL string that omits the offending parts; a caller treats any Unsupported
// as a rejection (it does not run a partial predicate).
func Lower(pred ir.Value) (Lowered, []Unsupported) {
	l := &lowering{}
	e := l.expr(pred)
	out := Lowered{Binds: l.binds}
	if e != nil {
		out.SQL = render(e)
	}
	return out, l.unsupported
}

// lowering accumulates the bind values and the unsupported nodes as it walks.
type lowering struct {
	binds       []Bind
	unsupported []Unsupported
}

func (l *lowering) reject(v ir.Value, reason string) sqlExpr {
	l.unsupported = append(l.unsupported, Unsupported{Node: v, Reason: reason})
	return nil
}

// expr lowers one value-graph node to a SQL expression node, recording a bind or
// an Unsupported as it goes. It returns nil for a node it rejects.
func (l *lowering) expr(v ir.Value) sqlExpr {
	switch n := v.(type) {
	case *ir.Call:
		return l.call(n)
	case *ir.FieldAccess:
		return l.column(n)
	case *ir.Adapt:
		// A literal flowing into a sized/typed column is wrapped in an Adapt; the
		// SQL value is the literal beneath it, bound by its own kind.
		return l.expr(n.Value)
	case *ir.IntLiteral:
		return l.intLiteral(n)
	case *ir.StringLiteral:
		l.binds = append(l.binds, Bind{Kind: BindText, Text: n.Value})
		return placeholder{}
	case *ir.BoolLiteral:
		l.binds = append(l.binds, Bind{Kind: BindBool, Bool: n.Value})
		return placeholder{}
	default:
		return l.reject(v, "expression")
	}
}

// The comparison operators whose null form is special (= NULL is never true, so
// equality and inequality against null become IS [NOT] NULL).
const (
	opEql = "eql"
	opNeq = "neq"
)

// call lowers a method call — the desugared form of every operator. The
// comparison, logical, and negation operators map to SQL; anything else (an
// arithmetic operator, a column or row method) is outside the core.
func (l *lowering) call(c *ir.Call) sqlExpr {
	switch c.Method {
	case "anan", "oror":
		return l.logical(c)
	case "not":
		if x := l.expr(c.Receiver); x != nil {
			return unary{x}
		}
		return nil
	}
	if _, ok := comparisonOps[c.Method]; ok {
		return l.compare(c)
	}
	return l.reject(c, "operator "+c.Method)
}

// logical lowers && / || to AND / OR over its two operands.
func (l *lowering) logical(c *ir.Call) sqlExpr {
	if len(c.Args) != 1 {
		return l.reject(c, "operator "+c.Method)
	}
	left := l.expr(c.Receiver)
	right := l.expr(c.Args[0])
	if left == nil || right == nil {
		return nil
	}
	op := "AND"
	if c.Method == "oror" {
		op = "OR"
	}
	return binary{op: op, l: left, r: right}
}

// compare lowers a comparison. Equality and inequality against a null literal
// become IS NULL / IS NOT NULL — SQL's = NULL is never true — while every other
// comparison is the plain operator over its two operands.
func (l *lowering) compare(c *ir.Call) sqlExpr {
	if len(c.Args) != 1 {
		return l.reject(c, "operator "+c.Method)
	}
	if isNull(c.Args[0]) {
		if c.Method != opEql && c.Method != opNeq {
			return l.reject(c, "null comparison")
		}
		if recv := l.expr(c.Receiver); recv != nil {
			return nullTest{x: recv, not: c.Method == opNeq}
		}
		return nil
	}
	left := l.expr(c.Receiver)
	right := l.expr(c.Args[0])
	if left == nil || right == nil {
		return nil
	}
	return binary{op: comparisonOps[c.Method], l: left, r: right}
}

// column lowers a column reference: a field read off self (an implicit-self
// read, lowered to self.field). A field read off anything else is not a single
// column of this table, so it is outside the core.
func (l *lowering) column(f *ir.FieldAccess) sqlExpr {
	if _, ok := f.Receiver.(*ir.SelfValue); !ok {
		return l.reject(f, "non-self field access")
	}
	return column{name: f.Field}
}

func (l *lowering) intLiteral(n *ir.IntLiteral) sqlExpr {
	v, ok := new(big.Int).SetString(n.Text, 0)
	if !ok {
		return l.reject(n, "integer literal")
	}
	l.binds = append(l.binds, Bind{Kind: BindInt, Int: v})
	return placeholder{}
}

// comparisonOps maps each comparison operator's method name to its SQL operator.
var comparisonOps = map[string]string{
	opEql: "=", opNeq: "<>", "lt": "<", "lteq": "<=", "gt": ">", "gteq": ">=",
}

// isNull reports whether a value is the null literal, seen through the Adapt a
// typed-null position wraps it in.
func isNull(v ir.Value) bool {
	if a, ok := v.(*ir.Adapt); ok {
		v = a.Value
	}
	_, ok := v.(*ir.NullValue)
	return ok
}

// sqlExpr is the small SQL expression AST the lowering builds before rendering —
// kept apart from the string so the rendering is one deterministic place
// (column quoting, operator spelling, parenthesization) the consumers share.
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

// render writes a SQL expression deterministically: a column is double-quoted, a
// placeholder is ?, and every compound is fully parenthesized so precedence is
// explicit and the output is stable for golden tests.
func render(e sqlExpr) string {
	var b strings.Builder
	write(&b, e)
	return b.String()
}

func write(b *strings.Builder, e sqlExpr) {
	switch n := e.(type) {
	case column:
		b.WriteByte('"')
		b.WriteString(n.name)
		b.WriteByte('"')
	case placeholder:
		b.WriteByte('?')
	case binary:
		b.WriteByte('(')
		write(b, n.l)
		b.WriteString(" " + n.op + " ")
		write(b, n.r)
		b.WriteByte(')')
	case unary:
		b.WriteString("(NOT ")
		write(b, n.x)
		b.WriteByte(')')
	case nullTest:
		b.WriteByte('(')
		write(b, n.x)
		if n.not {
			b.WriteString(" IS NOT NULL)")
		} else {
			b.WriteString(" IS NULL)")
		}
	}
}
