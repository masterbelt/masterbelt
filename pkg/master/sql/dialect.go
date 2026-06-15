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
}

// SQLite quotes with double quotes and uses ? placeholders.
var SQLite Dialect = ansiQuoted{placeholderFn: func(int) string { return "?" }}

// Postgres quotes with double quotes (ANSI) and uses $N positional placeholders.
var Postgres Dialect = ansiQuoted{placeholderFn: func(n int) string { return "$" + strconv.Itoa(n) }}

// MySQL quotes with backticks and uses ? placeholders.
var MySQL Dialect = mysqlQuoted{}

// ansiQuoted quotes identifiers the ANSI way — double quotes, an embedded quote
// doubled — shared by SQLite and PostgreSQL; the placeholder differs, so it is a
// field.
type ansiQuoted struct {
	placeholderFn func(int) string
}

func (ansiQuoted) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d ansiQuoted) Placeholder(n int) string { return d.placeholderFn(n) }

// mysqlQuoted quotes identifiers MySQL's way — backticks, an embedded backtick
// doubled.
type mysqlQuoted struct{}

func (mysqlQuoted) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (mysqlQuoted) Placeholder(int) string { return "?" }
