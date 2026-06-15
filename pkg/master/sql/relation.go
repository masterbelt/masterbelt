package sql

// Relation is a single-table query over a master's rows: an optional row filter
// (the where predicate) and the count a consumer reads from it. It is the shared
// query primitive the validation aggregates and scope build on — backend-neutral
// like Predicate, rendering per dialect — so neither assembles SQL by hand. A
// master used whole is the unfiltered relation; Where narrows it. Order, limit,
// joins, and the other aggregates (sum/min/max) are later slices on this same
// type; the minimal core is a filtered row count.
type Relation struct {
	where Predicate // the row filter; a zero Predicate matches every row
}

// All is the relation of every row of a master — no filter.
func All() Relation { return Relation{} }

// Where returns the relation narrowed to the rows the predicate holds for. The
// predicate is the same boolean fragment Lower produces, reused as the filter.
func (r Relation) Where(p Predicate) Relation {
	r.where = p
	return r
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
