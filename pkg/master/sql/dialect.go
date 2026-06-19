package sql

import (
	"strconv"
	"strings"
)

// Dialect is the part of SQL that differs between backends: how an identifier is
// quoted and how a bind placeholder is written. The operators the core emits
// (= <> < <= > >=, AND OR NOT, IS [NOT] NULL) are ANSI and shared, so they are
// not part of the dialect. A new backend adds a Dialect rather than forking the
// lowering — the predicate is lowered once, backend-neutral, and rendered per
// dialect.
type Dialect interface {
	// QuoteIdent quotes a column (or table) identifier, escaping the quote
	// character within it.
	QuoteIdent(name string) string
	// Placeholder writes the bind placeholder for the n-th bind (1-based, in the
	// order the placeholders appear).
	Placeholder(n int) string
	// LimitAll is the LIMIT count that imposes no cap — what an offset with no limit
	// renders against, since a skip needs a limit to skip from. It differs per
	// backend: SQLite's -1, PostgreSQL's ALL, MySQL's largest unsigned integer.
	LimitAll() string
}

// SQLite quotes with double quotes, uses ? placeholders, and spells the uncapped
// limit -1.
var SQLite Dialect = ansiQuoted{placeholderFn: func(int) string { return "?" }, limitAll: "-1"}

// Postgres quotes with double quotes (ANSI), uses $N positional placeholders, and
// spells the uncapped limit ALL.
var Postgres Dialect = ansiQuoted{placeholderFn: func(n int) string { return "$" + strconv.Itoa(n) }, limitAll: "ALL"}

// MySQL quotes with backticks and uses ? placeholders.
var MySQL Dialect = mysqlQuoted{}

// ansiQuoted quotes identifiers the ANSI way — double quotes, an embedded quote
// doubled — shared by SQLite and PostgreSQL; the placeholder and uncapped-limit
// spelling differ, so they are fields.
type ansiQuoted struct {
	placeholderFn func(int) string
	limitAll      string
}

func (ansiQuoted) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d ansiQuoted) Placeholder(n int) string { return d.placeholderFn(n) }
func (d ansiQuoted) LimitAll() string         { return d.limitAll }

// mysqlQuoted quotes identifiers MySQL's way — backticks, an embedded backtick
// doubled.
type mysqlQuoted struct{}

func (mysqlQuoted) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (mysqlQuoted) Placeholder(int) string { return "?" }

// LimitAll is MySQL's largest unsigned integer, the count its manual recommends to
// retrieve all rows after an offset (it has no LIMIT ALL and requires a nonnegative
// limit argument).
func (mysqlQuoted) LimitAll() string { return "18446744073709551615" }
