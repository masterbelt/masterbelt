package sql_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/master/sql"
)

// TestRelationCountAll pins the unfiltered count: a relation with no filter
// renders a plain count over the table, no WHERE and no binds.
func TestRelationCountAll(t *testing.T) {
	got, binds := sql.All().CountSQL("rows", sql.SQLite)
	if want := `SELECT count(*) FROM "rows"`; got != want {
		t.Errorf("CountSQL = %q, want %q", got, want)
	}
	if len(binds) != 0 {
		t.Errorf("binds = %d, want 0", len(binds))
	}
}

// TestRelationCountWhere pins the filtered count: the where predicate (the same
// fragment Lower produces) becomes the WHERE clause, and its binds travel with the
// query. The fragment is the lowering's, so it is parenthesized and dialect-quoted.
func TestRelationCountWhere(t *testing.T) {
	pred, unsupported := lowerProbe(t, "", "id: int, power: int", "c.power > 0")
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	got, binds := sql.All().Where(pred).CountSQL("rows", sql.SQLite)
	if want := `SELECT count(*) FROM "rows" WHERE ("power" > ?)`; got != want {
		t.Errorf("CountSQL = %q, want %q", got, want)
	}
	if b := bindsString(binds); b != "[int 0]" {
		t.Errorf("binds = %s, want [int 0]", b)
	}
}

// TestRelationRowKeysAll pins the unfiltered row-key select: every key, ordered by
// the key for deterministic (insert) order, no WHERE, no LIMIT, no binds.
func TestRelationRowKeysAll(t *testing.T) {
	got, binds := sql.All().RowKeysSQL("k", "rows", sql.SQLite)
	if want := `SELECT "k" FROM "rows" ORDER BY "k"`; got != want {
		t.Errorf("RowKeysSQL = %q, want %q", got, want)
	}
	if len(binds) != 0 {
		t.Errorf("binds = %d, want 0", len(binds))
	}
}

// TestRelationRowKeysWhereLimit pins the filtered, capped row-key select: the filter
// is the WHERE clause (with its binds), the order is by the key, and the limit is a
// rendered integer literal after ORDER BY.
func TestRelationRowKeysWhereLimit(t *testing.T) {
	pred, unsupported := lowerProbe(t, "", "id: int, power: int", "c.power > 0")
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	got, binds := sql.All().Where(pred).Limit(2).RowKeysSQL("k", "rows", sql.SQLite)
	if want := `SELECT "k" FROM "rows" WHERE ("power" > ?) ORDER BY "k" LIMIT 2`; got != want {
		t.Errorf("RowKeysSQL = %q, want %q", got, want)
	}
	if b := bindsString(binds); b != "[int 0]" {
		t.Errorf("binds = %s, want [int 0]", b)
	}
}

// TestRelationLimitKeepsSmaller pins that re-limiting keeps the smaller cap, so the
// row count never widens past either limit — limit(5).limit(2) and limit(2).limit(5)
// both cap at two — and a negative limit floors at zero.
func TestRelationLimitKeepsSmaller(t *testing.T) {
	for _, c := range []struct {
		name string
		rel  sql.Relation
		want string
	}{
		{"five then two", sql.All().Limit(5).Limit(2), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT 2`},
		{"two then five", sql.All().Limit(2).Limit(5), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT 2`},
		{"negative floors to zero", sql.All().Limit(-3), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT 0`},
	} {
		if got, _ := c.rel.RowKeysSQL("k", "rows", sql.SQLite); got != c.want {
			t.Errorf("%s: RowKeysSQL = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRelationRowKeysOrder pins the row-key select with sort keys: each key renders in
// priority order (descending carries DESC, ascending bare), then the synthetic key as
// the final tiebreaker so the order stays total and deterministic.
func TestRelationRowKeysOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		rel  sql.Relation
		want string
	}{
		{"descending", sql.All().OrderBy("power", true), `SELECT "k" FROM "rows" ORDER BY "power" DESC, "k"`},
		{"ascending", sql.All().OrderBy("power", false), `SELECT "k" FROM "rows" ORDER BY "power", "k"`},
		{"primary then tiebreak", sql.All().OrderBy("power", true).OrderBy("id", false), `SELECT "k" FROM "rows" ORDER BY "power" DESC, "id", "k"`},
		{"order then limit", sql.All().OrderBy("power", true).Limit(2), `SELECT "k" FROM "rows" ORDER BY "power" DESC, "k" LIMIT 2`},
	} {
		if got, _ := c.rel.RowKeysSQL("k", "rows", sql.SQLite); got != c.want {
			t.Errorf("%s: RowKeysSQL = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRelationRowKeysOffset pins the row-key select with a skip: an offset renders an
// OFFSET after the limit, and an offset with no limit renders the no-limit cap (-1) so
// SQL has a limit to skip against. Skips accumulate.
func TestRelationRowKeysOffset(t *testing.T) {
	for _, c := range []struct {
		name string
		rel  sql.Relation
		want string
	}{
		{"offset with limit", sql.All().Limit(2).Offset(1), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT 2 OFFSET 1`},
		{"offset no limit", sql.All().Offset(3), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT -1 OFFSET 3`},
		{"offset accumulates", sql.All().Offset(2).Offset(3), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT -1 OFFSET 5`},
		{"limit only no offset", sql.All().Limit(2), `SELECT "k" FROM "rows" ORDER BY "k" LIMIT 2`},
	} {
		if got, _ := c.rel.RowKeysSQL("k", "rows", sql.SQLite); got != c.want {
			t.Errorf("%s: RowKeysSQL = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRelationWhereIntersects pins that narrowing a relation that already has a
// filter keeps both — the predicates are conjoined with AND and their binds
// merged in order — rather than the second replacing the first. A consumer that
// further filters a scoped relation must not silently drop the scope.
func TestRelationWhereIntersects(t *testing.T) {
	const fields = "id: int, power: int, cost: int"
	p1, u1 := lowerProbe(t, "", fields, "c.power > 0")
	p2, u2 := lowerProbe(t, "", fields, "c.cost < 100")
	if len(u1)+len(u2) != 0 {
		t.Fatalf("predicates did not lower: %+v %+v", u1, u2)
	}
	got, binds := sql.All().Where(p1).Where(p2).CountSQL("rows", sql.SQLite)
	if want := `SELECT count(*) FROM "rows" WHERE (("power" > ?) AND ("cost" < ?))`; got != want {
		t.Errorf("CountSQL = %q, want %q", got, want)
	}
	if b := bindsString(binds); b != "[int 0, int 100]" {
		t.Errorf("binds = %s, want [int 0, int 100]", b)
	}
}

// TestRelationCountDialects pins that one relation renders per backend, like the
// predicate: the count and the WHERE are shared, while identifier quoting and the
// placeholder follow the dialect.
func TestRelationCountDialects(t *testing.T) {
	pred, unsupported := lowerProbe(t, "", "id: int, power: int, cost: int", "c.power >= c.cost")
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	rel := sql.All().Where(pred)
	cases := []struct {
		dialect sql.Dialect
		want    string
	}{
		{sql.SQLite, `SELECT count(*) FROM "rows" WHERE ("power" >= "cost")`},
		{sql.Postgres, `SELECT count(*) FROM "rows" WHERE ("power" >= "cost")`},
		{sql.MySQL, "SELECT count(*) FROM `rows` WHERE (`power` >= `cost`)"},
	}
	for _, tc := range cases {
		if got, _ := rel.CountSQL("rows", tc.dialect); got != tc.want {
			t.Errorf("CountSQL = %q, want %q", got, tc.want)
		}
	}
}
