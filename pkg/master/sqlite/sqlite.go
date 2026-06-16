// Package sqlite runs a master's typed rows through an in-memory SQLite database
// so a lowered row predicate can be evaluated in SQL. It is the execution half of
// the DSL→SQL pipeline: the sql package lowers a predicate to backend-neutral text
// and generates the table's DDL, and this package loads the rows and runs the
// query. It is where the SQLite driver dependency is sealed in — the master core
// imports only the IR and the diagnostics, and a code generator or another
// consumer reaches the engine here rather than the driver directly.
//
// The database is in-memory and the rows are inserted in source order, keyed by a
// synthetic row-index column, so a query ordered by that key is deterministic; a
// golden over the violations it reports is stable. The engine is the basis the
// aggregate, scope, and reference checks will
// run on; the per-row validate the language already evaluates row by row stays on
// its evaluator and is not rebuilt here.
package sqlite

import (
	"database/sql"
	"fmt"
	"math/big"

	_ "modernc.org/sqlite" // the pure-Go SQLite driver, registered as "sqlite"

	"github.com/masterbelt/masterbelt/pkg/master"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// tableName is the fixed identifier the single master under validation is loaded
// under. It is internal and never shown, so a constant safe identifier.
const tableName = "rows"

// dialect is the engine's SQL dialect — SQLite, the one it executes. The lowering
// is backend-neutral; the engine pins the one backend it runs.
var dialect = mastersql.SQLite

// Engine holds one master's rows in an in-memory SQLite database so a lowered
// predicate can be run against them. Close releases the database.
type Engine struct {
	db      *sql.DB
	keyCol  string       // synthetic row-index column the violations map back through
	columns []ir.Field   // the table's columns — the synthetic key first, then the data columns
	rows    []master.Row // kept so a violating row maps back to its source cell
}

// Violation is a row that does not satisfy a predicate. Row is the zero-based
// index into the loaded rows; Origin is the row's locator — its first column's
// cell, the path:row,col a data check anchors to. Pointing at the precise cell a
// multi-column predicate faults is a later generalization.
type Violation struct {
	Row    int
	Origin master.Origin
}

// Load opens an in-memory database, creates a table for the master's loaded
// columns, and inserts its rows in order. The columns are the typed table's — the
// scalar fields the loader bound — looked up against fields for their types, with
// a synthetic row-index key column prepended (named so it cannot collide with a
// master's own column). The caller must Close the returned Engine. An integer
// value outside SQLite's 64-bit range is reported rather than silently truncated;
// arbitrary-precision storage is a later concern.
func Load(fields []ir.Field, table master.Table) (*Engine, error) {
	data := alignColumns(fields, table.Columns)
	keyCol := uniqueName("_mb_row", table.Columns)
	columns := append([]ir.Field{{Name: keyCol, Type: &ir.Builtin{Name: "int"}}}, data...)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	// A :memory: database is private to its connection, so a second connection
	// from the pool would see an empty database with no table; pinning the pool to
	// one connection keeps every query on the one the rows were loaded into.
	db.SetMaxOpenConns(1)
	e := &Engine{db: db, keyCol: keyCol, columns: columns, rows: table.Rows}
	if err := e.create(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := e.insert(table); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

// Close releases the in-memory database.
func (e *Engine) Close() error { return e.db.Close() }

// Violations runs the predicate and returns the rows it does not hold for, in row
// order. A row violates the predicate when the predicate is not definitely true —
// false, or NULL under SQL's three-valued logic (a comparison touching a NULL
// column). That is the fail-safe a data check wants, and the same rule the per-row
// evaluator follows: a row a check cannot confirm valid is treated as failing
// rather than passing silently. An empty predicate (the lowering produced no
// expression) holds for every row.
func (e *Engine) Violations(pred mastersql.Predicate) ([]Violation, error) {
	frag := pred.SQL(dialect)
	if frag == "" {
		return nil, nil
	}
	args, err := bindArgs(pred.Binds())
	if err != nil {
		return nil, err
	}
	// (frag) IS NOT 1 selects the rows the predicate is not definitely true for:
	// false rows, and rows where it is NULL (IS NOT is null-aware, so NULL IS NOT 1
	// is true). The synthetic key is selected rather than SQLite's implicit rowid —
	// a master may declare a column literally named rowid, which would shadow the
	// implicit one and return user data instead of the insert position — and
	// ordering by it keeps the result stable.
	key := dialect.QuoteIdent(e.keyCol)
	query := "SELECT " + key + " FROM " + dialect.QuoteIdent(tableName) +
		" WHERE (" + frag + ") IS NOT 1 ORDER BY " + key
	rows, err := e.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Violation
	for rows.Next() {
		var idx int64
		if err := rows.Scan(&idx); err != nil {
			return nil, err
		}
		out = append(out, Violation{Row: int(idx), Origin: e.originOf(int(idx))})
	}
	return out, rows.Err()
}

// Count runs the relation's row count against the loaded table and returns it.
// An unfiltered relation counts every row; a filtered one counts the rows its
// predicate is true for. It is the scalar path the validation aggregates and
// scope read — the engine runs the relation's SQL the sql package renders, so the
// one relation-to-SQL definition serves both the validation engine and codegen.
func (e *Engine) Count(rel mastersql.Relation) (int64, error) {
	query, binds := rel.CountSQL(tableName, dialect)
	args, err := bindArgs(binds)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Sum runs the relation's sum of a column against the loaded rows and returns the
// scalar — the aggregate twin of Count, over the same relation-to-SQL the sql
// package renders. An empty relation sums to zero (the SQL COALESCEs NULL away).
func (e *Engine) Sum(rel mastersql.Relation, column string) (int64, error) {
	query, binds := rel.SumSQL(column, tableName, dialect)
	args, err := bindArgs(binds)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// create builds the single table from the loaded columns. A master always
// declares at least one field, so the column list is never empty.
func (e *Engine) create() error {
	_, err := e.db.Exec(mastersql.CreateTable(tableName, e.columns, dialect))
	return err
}

// insert loads every row in order, binding the synthetic key to the row's index
// so a violation maps straight back to its source row.
func (e *Engine) insert(table master.Table) error {
	stmt := mastersql.InsertInto(tableName, e.columns, dialect)
	dataCols := len(e.columns) - 1 // the columns past the synthetic key
	for i, row := range table.Rows {
		args, err := rowArgs(i, row, dataCols)
		if err != nil {
			return err
		}
		if _, err := e.db.Exec(stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

// originOf is the source origin a violating row anchors to: its first cell's, the
// row's locator. A row with no cells has no origin to give.
func (e *Engine) originOf(idx int) master.Origin {
	if idx < 0 || idx >= len(e.rows) {
		return master.Origin{}
	}
	if cells := e.rows[idx].Cells; len(cells) > 0 {
		return cells[0].Origin
	}
	return master.Origin{}
}

// alignColumns builds the table's columns in the typed table's column order, each
// with its declared type from fields. The loader's coercion emits columns in field
// order, a subset of fields (the scalar ones it bound), so every name resolves.
func alignColumns(fields []ir.Field, columns []string) []ir.Field {
	byName := make(map[string]ir.Type, len(fields))
	for _, f := range fields {
		byName[f.Name] = f.Type
	}
	out := make([]ir.Field, len(columns))
	for i, name := range columns {
		out[i] = ir.Field{Name: name, Type: byName[name]}
	}
	return out
}

// uniqueName returns base, or base with underscores appended until it matches none
// of the taken names — so the engine's synthetic key column cannot be shadowed by a
// master's own column of the same name.
func uniqueName(base string, taken []string) string {
	set := make(map[string]bool, len(taken))
	for _, c := range taken {
		set[c] = true
	}
	name := base
	for set[name] {
		name += "_"
	}
	return name
}

// rowArgs converts a row to positional bind arguments: the synthetic key (the
// row's index) followed by its cell values, in column order. A row whose cell
// count does not match the data columns is a malformed table and an error rather
// than a silently short INSERT.
func rowArgs(index int, row master.Row, dataCols int) ([]any, error) {
	if len(row.Cells) != dataCols {
		return nil, fmt.Errorf("row has %d cells, want %d columns", len(row.Cells), dataCols)
	}
	args := make([]any, 0, dataCols+1)
	args = append(args, int64(index))
	for _, c := range row.Cells {
		a, err := constArg(c.Value)
		if err != nil {
			return nil, err
		}
		args = append(args, a)
	}
	return args, nil
}

// bindArgs converts a predicate's bind values to positional arguments.
func bindArgs(binds []mastersql.Bind) ([]any, error) {
	args := make([]any, len(binds))
	for i, b := range binds {
		switch b.Kind {
		case mastersql.BindInt:
			a, err := intArg(b.Int)
			if err != nil {
				return nil, err
			}
			args[i] = a
		case mastersql.BindText:
			args[i] = b.Text
		case mastersql.BindBool:
			args[i] = boolArg(b.Bool)
		default:
			return nil, fmt.Errorf("unsupported bind kind %v", b.Kind)
		}
	}
	return args, nil
}

// constArg converts a typed cell value to a SQLite argument. A gap cell (a nil
// value the coercion could not fill) binds as SQL NULL, so a predicate over it is
// NULL and the row reads as a violation — the fail-safe again. An integer, bool,
// or string binds as its scalar; any other constant kind is not a column value the
// engine stores.
func constArg(c *ir.Constant) (any, error) {
	if c == nil {
		return nil, nil
	}
	switch c.Kind {
	case ir.ConstInt:
		return intArg(c.Int)
	case ir.ConstBool:
		return boolArg(c.Bool), nil
	case ir.ConstString:
		return c.Str, nil
	default:
		return nil, fmt.Errorf("unsupported constant kind %v", c.Kind)
	}
}

// intArg narrows an arbitrary-precision integer to the int64 SQLite stores. A
// value outside that range is reported rather than silently truncated — full
// bignum storage is a later concern, not a wrong answer now.
func intArg(n *big.Int) (any, error) {
	if n == nil {
		return nil, nil
	}
	if !n.IsInt64() {
		return nil, fmt.Errorf("integer %s is outside SQLite's 64-bit range", n)
	}
	return n.Int64(), nil
}

// boolArg renders a bool as the 0/1 SQLite stores it as.
func boolArg(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
