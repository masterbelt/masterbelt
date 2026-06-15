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
	pred, unsupported := lowerValidate(t, "id: int, power: int", "self.power > 0")
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

// TestRelationCountDialects pins that one relation renders per backend, like the
// predicate: the count and the WHERE are shared, while identifier quoting and the
// placeholder follow the dialect.
func TestRelationCountDialects(t *testing.T) {
	pred, unsupported := lowerValidate(t, "id: int, power: int, cost: int", "self.power >= self.cost")
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
