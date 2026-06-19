package sql

import "strconv"

// Relation is a single-table query over a master's rows: an optional row filter
// (the where predicate) and an optional row cap (the limit). It is the shared
// query primitive the validation aggregates and scope build on — backend-neutral
// like Predicate, rendering per dialect — so neither assembles SQL by hand. A
// master used whole is the unfiltered relation; Where narrows it, Limit caps it.
// Order, joins, and the other aggregates (min/max) are later slices on this same
// type.
type Relation struct {
	where   Predicate // the row filter; a zero Predicate matches every row
	limit   int64     // the row cap, valid when limited
	limited bool      // whether a limit is set
}

// All is the relation of every row of a master — no filter.
func All() Relation { return Relation{} }

// Where returns the relation narrowed to the rows the predicate holds for. The
// predicate is the same boolean fragment Lower produces, reused as the filter.
// Narrowing a relation that already carries a filter intersects the two rather
// than replacing the first — so a scoped relation further filtered keeps its scope
// — by conjoining them with AND.
func (r Relation) Where(p Predicate) Relation {
	r.where = r.where.and(p)
	return r
}

// Limit returns the relation capped to at most n rows. Limiting a relation that
// already carries a cap keeps the smaller of the two, the row count both caps
// allow — limit(5).limit(2) and limit(2).limit(5) both keep at most two rows —
// so a re-limited relation never widens past either cap. A negative n is treated
// as zero (no rows), the floor a row count cannot fall below.
func (r Relation) Limit(n int64) Relation {
	if n < 0 {
		n = 0
	}
	if r.limited && r.limit < n {
		return r
	}
	r.limit, r.limited = n, true
	return r
}

// RowKeysSQL renders a select of the relation's row keys — the engine's synthetic
// insert-position key — for a dialect: the keys the filter keeps, ordered by the
// key so the result is deterministic (insert order), and capped when the relation
// carries a limit. The caller materializes the full rows from those keys. The
// binds are the filter's; the limit is a rendered integer literal, not a bind.
func (r Relation) RowKeysSQL(keyCol, table string, d Dialect) (string, []Bind) {
	q := "SELECT " + d.QuoteIdent(keyCol) + " FROM " + d.QuoteIdent(table)
	frag := r.where.SQL(d)
	if frag != "" {
		q += " WHERE " + frag
	}
	q += " ORDER BY " + d.QuoteIdent(keyCol)
	if r.limited {
		q += " LIMIT " + strconv.FormatInt(r.limit, 10)
	}
	if frag == "" {
		return q, nil
	}
	return q, r.where.Binds()
}

// CountSQL renders the count of the relation's rows for a dialect: a count over
// the table, with the filter as a WHERE clause when the relation has one. SQL's
// WHERE keeps the rows the predicate is true for (a null or false drops the row),
// the matching-count semantics a count wants — distinct from a validation, which
// fails the rows a predicate is not definitely true for. The binds are the
// filter's, positional and dialect-independent.
func (r Relation) CountSQL(table string, d Dialect) (string, []Bind) {
	sel := "SELECT count(*) FROM " + d.QuoteIdent(table)
	frag := r.where.SQL(d)
	if frag == "" {
		return sel, nil
	}
	return sel + " WHERE " + frag, r.where.Binds()
}

// ColumnSQL renders a projection of one column over the relation's rows for a
// dialect, with the filter as a WHERE clause when the relation has one. The engine
// reads the column's values to accumulate an aggregate (a sum) in arbitrary
// precision itself rather than asking SQL for the total: SQL's integer sum overflows
// the fixed-width scalar it returns, while the relation's sum widens to nint. The
// binds are the filter's, positional and dialect-neutral.
func (r Relation) ColumnSQL(column, table string, d Dialect) (string, []Bind) {
	sel := "SELECT " + d.QuoteIdent(column) + " FROM " + d.QuoteIdent(table)
	frag := r.where.SQL(d)
	if frag == "" {
		return sel, nil
	}
	return sel + " WHERE " + frag, r.where.Binds()
}
