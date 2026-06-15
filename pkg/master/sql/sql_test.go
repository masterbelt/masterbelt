package sql_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/master/sql"
)

// lowerSrc resolves a full master source and lowers its first per-row validate
// predicate to SQL — the real value graph the pipeline produces, not a hand-built
// one.
func lowerSrc(t *testing.T, src string) (sql.Lowered, []sql.Unsupported) {
	t.Helper()
	prog := semantic.NewProgram()
	prog.SetFile("m.belt", abstract.NewDocument([]byte(src)), nil)
	prog.Refresh()
	m := prog.Module("m.belt")
	if m == nil {
		t.Fatalf("no module for %q", src)
	}
	for _, def := range m.Types {
		if def.Master != nil && len(def.Master.RowChecks) > 0 {
			return sql.Lower(def.Master.RowChecks[0].Cond)
		}
	}
	t.Fatalf("no row check resolved for %q", src)
	return sql.Lowered{}, nil
}

// lowerValidate builds a master whose per-row validate asserts cond over the
// given record fields, then lowers that predicate.
func lowerValidate(t *testing.T, fields, cond string) (sql.Lowered, []sql.Unsupported) {
	t.Helper()
	return lowerSrc(t, "master M {\n  record { "+fields+" }\n  primary id\n"+
		"  validate {\n    each {\n      assert "+cond+"\n    }\n  }\n}\n")
}

// bindsString renders bind values compactly for golden comparison.
func bindsString(bs []sql.Bind) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		switch b.Kind {
		case sql.BindInt:
			parts[i] = "int " + b.Int.String()
		case sql.BindText:
			parts[i] = fmt.Sprintf("text %q", b.Text)
		case sql.BindBool:
			parts[i] = fmt.Sprintf("bool %v", b.Bool)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// TestLowerCore pins the core predicate vocabulary lowering to single-table SQL:
// comparisons, logical operators, negation, column references, and literals
// (parameterized), with each generated fragment and its binds fixed as a golden.
func TestLowerCore(t *testing.T) {
	const fields = "id: int, power: int, cost: int, name: string, active: bool"
	cases := []struct {
		name, cond, sql, binds string
	}{
		{"column vs column", "self.power >= self.cost", `("power" >= "cost")`, "[]"},
		{"column vs int", "self.power >= 1", `("power" >= ?)`, "[int 1]"},
		{"equality", "self.power == 0", `("power" = ?)`, "[int 0]"},
		{"inequality", "self.cost != 2", `("cost" <> ?)`, "[int 2]"},
		{"less / greater", "self.power < 10", `("power" < ?)`, "[int 10]"},
		{"string equality", "self.name == \"fire\"", `("name" = ?)`, `[text "fire"]`},
		{"bool equality", "self.active == true", `("active" = ?)`, "[bool true]"},
		{"logical and", "self.power >= 1 && self.cost != 2", `(("power" >= ?) AND ("cost" <> ?))`, "[int 1, int 2]"},
		{"logical or", "self.power == 0 || self.cost == 0", `(("power" = ?) OR ("cost" = ?))`, "[int 0, int 0]"},
		{"negation", "!(self.power >= 1)", `(NOT ("power" >= ?))`, "[int 1]"},
		{"nested", "self.power >= 1 && (self.cost == 0 || self.cost == 2)", `(("power" >= ?) AND (("cost" = ?) OR ("cost" = ?)))`, "[int 1, int 0, int 2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unsupported := lowerValidate(t, fields, tc.cond)
			if len(unsupported) != 0 {
				t.Fatalf("unexpected unsupported nodes: %+v", unsupported)
			}
			if got.SQL != tc.sql {
				t.Errorf("SQL = %q, want %q", got.SQL, tc.sql)
			}
			if b := bindsString(got.Binds); b != tc.binds {
				t.Errorf("binds = %s, want %s", b, tc.binds)
			}
		})
	}
}

// TestLowerNull pins that equality and inequality against null become IS NULL and
// IS NOT NULL, with no bind — SQL's = NULL is never true.
func TestLowerNull(t *testing.T) {
	const fields = "id: int, opt: int | null"
	t.Run("is null", func(t *testing.T) {
		got, u := lowerValidate(t, fields, "self.opt == null")
		if len(u) != 0 {
			t.Fatalf("unsupported: %+v", u)
		}
		if got.SQL != `("opt" IS NULL)` || len(got.Binds) != 0 {
			t.Errorf("got %q binds %s, want (\"opt\" IS NULL) []", got.SQL, bindsString(got.Binds))
		}
	})
	t.Run("is not null", func(t *testing.T) {
		got, u := lowerValidate(t, fields, "self.opt != null")
		if len(u) != 0 {
			t.Fatalf("unsupported: %+v", u)
		}
		if got.SQL != `("opt" IS NOT NULL)` || len(got.Binds) != 0 {
			t.Errorf("got %q binds %s, want (\"opt\" IS NOT NULL) []", got.SQL, bindsString(got.Binds))
		}
	})
}

// TestLowerUnsupported pins that a predicate outside the core — an arithmetic
// operator, a row method — is reported as Unsupported rather than silently
// dropped or mis-lowered, so the caller rejects it.
func TestLowerUnsupported(t *testing.T) {
	t.Run("arithmetic operand", func(t *testing.T) {
		_, u := lowerValidate(t, "id: int, power: int, cost: int", "self.power + self.cost > 0")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an arithmetic operand")
		}
	})
	t.Run("division", func(t *testing.T) {
		_, u := lowerValidate(t, "id: int", "100 / self.id > 0")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for division")
		}
	})
	t.Run("row method", func(t *testing.T) {
		// A row method resolves (the impl provides it) but its body is arbitrary
		// belt, not a single-table SQL expression, so the lowering rejects it.
		src := "master M {\n  record { id: int } impl {\n    pub ok(): bool {\n      return self.id > 0\n    }\n  }\n" +
			"  primary id\n  validate {\n    each {\n      assert self.ok()\n    }\n  }\n}\n"
		_, u := lowerSrc(t, src)
		if len(u) == 0 {
			t.Fatal("want an unsupported node for a row method call")
		}
	})
}
