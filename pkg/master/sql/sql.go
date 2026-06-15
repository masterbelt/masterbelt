// Package sql lowers a row predicate — a belt boolean expression over a master's
// row (self) — to a single-table SQL boolean expression. It is the shared
// DSL→SQL primitive the validation engine, scope, and the sqlite-backed code
// generators all build on, so it is kept to one form and not forked.
//
// The lowering is a pure function over the resolved value graph (ir.Value): it
// imports only the IR it reads, never a SQL driver. It produces a backend-neutral
// Predicate that renders to a dialect's SQL string (SQLite, PostgreSQL, MySQL —
// the dialect supplies identifier quoting and the bind placeholder) with the bind
// values carried out of band; the SQL is executed elsewhere. A node it does not
// lower — an arithmetic operator, a column method, a row method, anything outside
// the comparison/logical/column/literal core — is returned as an Unsupported, for
// the caller to report against its own source provenance rather than be silently
// dropped.
//
// The package is split by responsibility: this file holds the public result
// types; lower.go walks the value graph into the SQL AST and rejects what the
// core does not express; expr.go is that AST and its deterministic rendering; and
// dialect.go is the per-backend quoting and placeholders.
package sql

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Predicate is a lowered row predicate, held in a backend-neutral form: an
// internal SQL expression tree plus the bind values, in the order they appear.
// The dialect-specific text is produced by SQL — so one lowering renders for
// SQLite, PostgreSQL, or MySQL — while the binds are the same for every dialect.
// It is a boolean fragment: the SELECT, the WHERE/NOT wrapper a validation needs,
// and the ORDER/LIMIT a scope adds are the caller's.
type Predicate struct {
	root  sqlExpr
	binds []Bind
}

// SQL renders the predicate for a dialect. An empty string means the lowering
// produced no expression (a caller checks Lower's Unsupported result, not this).
func (p Predicate) SQL(d Dialect) string {
	if p.root == nil {
		return ""
	}
	return render(p.root, d)
}

// Binds are the predicate's bind values, positional and dialect-independent.
func (p Predicate) Binds() []Bind { return p.binds }

// and returns the conjunction of two predicates, so a consumer intersecting two
// filters (a scope's and an aggregate's) gets both rather than the second alone.
// An empty predicate matches every row, so it is the identity and drops out of the
// conjunction. The binds concatenate in operand order — left then right — which is
// the order the rendered placeholders appear in, so they stay aligned.
func (p Predicate) and(q Predicate) Predicate {
	switch {
	case p.root == nil:
		return q
	case q.root == nil:
		return p
	default:
		binds := make([]Bind, 0, len(p.binds)+len(q.binds))
		binds = append(binds, p.binds...)
		binds = append(binds, q.binds...)
		return Predicate{root: binary{op: "AND", l: p.root, r: q.root}, binds: binds}
	}
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
