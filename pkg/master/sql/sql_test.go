package sql_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// lowerSrc resolves a full master source and lowers its first per-row validate
// predicate to SQL — the real value graph the pipeline produces, not a hand-built
// one.
func lowerSrc(t *testing.T, src string) (sql.Predicate, []sql.Unsupported) {
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
			return sql.Lower(def.Master.RowChecks[0].Cond, rowFields(def.Master.Row))
		}
	}
	t.Fatalf("no row check resolved for %q", src)
	return sql.Predicate{}, nil
}

// rowFields extracts a master row's stored columns from its record type.
func rowFields(row ir.Type) []ir.Field {
	if rec, ok := row.(*ir.Record); ok {
		return rec.Fields
	}
	return nil
}

// lowerValidate builds a master whose per-row validate asserts cond over the
// given record fields, then lowers that predicate.
func lowerValidate(t *testing.T, fields, cond string) (sql.Predicate, []sql.Unsupported) {
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
			if s := got.SQL(sql.SQLite); s != tc.sql {
				t.Errorf("SQL = %q, want %q", s, tc.sql)
			}
			if b := bindsString(got.Binds()); b != tc.binds {
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
		if s := got.SQL(sql.SQLite); s != `("opt" IS NULL)` || len(got.Binds()) != 0 {
			t.Errorf("got %q binds %s, want (\"opt\" IS NULL) []", s, bindsString(got.Binds()))
		}
	})
	t.Run("is not null", func(t *testing.T) {
		got, u := lowerValidate(t, fields, "self.opt != null")
		if len(u) != 0 {
			t.Fatalf("unsupported: %+v", u)
		}
		if s := got.SQL(sql.SQLite); s != `("opt" IS NOT NULL)` || len(got.Binds()) != 0 {
			t.Errorf("got %q binds %s, want (\"opt\" IS NOT NULL) []", s, bindsString(got.Binds()))
		}
	})
}

// TestDialects pins that one lowering renders per backend: the binds and the
// operators are shared, while identifier quoting and the bind placeholder follow
// the dialect — SQLite double-quotes with ?, PostgreSQL double-quotes with $N,
// MySQL backtick-quotes with ?. This is what lets a backend be chosen without
// forking the lowering.
func TestDialects(t *testing.T) {
	got, u := lowerValidate(t, "id: int, power: int, cost: int", "self.power >= 1 && self.cost != 2")
	if len(u) != 0 {
		t.Fatalf("unsupported: %+v", u)
	}
	cases := []struct {
		dialect sql.Dialect
		want    string
	}{
		{sql.SQLite, `(("power" >= ?) AND ("cost" <> ?))`},
		{sql.Postgres, `(("power" >= $1) AND ("cost" <> $2))`},
		{sql.MySQL, "((`power` >= ?) AND (`cost` <> ?))"},
	}
	for _, tc := range cases {
		if s := got.SQL(tc.dialect); s != tc.want {
			t.Errorf("SQL = %q, want %q", s, tc.want)
		}
	}
	// The binds are the same for every dialect.
	if b := bindsString(got.Binds()); b != "[int 1, int 2]" {
		t.Errorf("binds = %s, want [int 1, int 2]", b)
	}
}

// TestQuoteEscaping pins that the dialects escape an embedded quote character in
// an identifier — a double quote doubled for ANSI, a backtick doubled for MySQL —
// so a column name carrying the quote character cannot break out of the quoting.
func TestQuoteEscaping(t *testing.T) {
	if got := sql.SQLite.QuoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("SQLite quote = %q, want %q", got, `"we""ird"`)
	}
	if got := sql.MySQL.QuoteIdent("we`ird"); got != "`we``ird`" {
		t.Errorf("MySQL quote = %q, want %q", got, "`we``ird`")
	}
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
	t.Run("getter read", func(t *testing.T) {
		// A getter read surfaces as a field access but holds no table column, so it
		// is rejected rather than emitted as a column that does not exist.
		src := "master M {\n  record { id: int } impl {\n    pub get positive(): bool {\n      return self.id > 0\n    }\n  }\n" +
			"  primary id\n  validate {\n    each {\n      assert positive\n    }\n  }\n}\n"
		_, u := lowerSrc(t, src)
		if len(u) == 0 {
			t.Fatal("want an unsupported node for a getter read")
		}
	})
	t.Run("overridden operator", func(t *testing.T) {
		// A column whose type overrides the comparison operator does not carry the
		// builtin's SQL semantics, so the comparison is rejected, not emitted as =.
		src := "type Weird = int impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n" +
			"master M {\n  record { w: Weird }\n  primary w\n  validate {\n    each {\n      assert self.w == self.w\n    }\n  }\n}\n"
		_, u := lowerSrc(t, src)
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an overridden operator")
		}
	})
	t.Run("overridden operator on a generic type", func(t *testing.T) {
		// The override check must see through a generic application (Weird<string>
		// is an applied nominal, not a bare one), or the custom operator slips past.
		src := "type Weird<T> = int impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n" +
			"master M {\n  record { w: Weird<string> }\n  primary w\n  validate {\n    each {\n      assert self.w == self.w\n    }\n  }\n}\n"
		_, u := lowerSrc(t, src)
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an overridden operator on a generic type")
		}
	})
	t.Run("overridden logical operator", func(t *testing.T) {
		// A bool-like column type that overrides not/&&/|| does not carry the
		// builtin's semantics either, so the logical operator is rejected too.
		src := "type Weird = bool impl {\n  pub not(): bool {\n    return true\n  }\n}\n" +
			"master M {\n  record { w: Weird }\n  primary w\n  validate {\n    each {\n      assert !self.w\n    }\n  }\n}\n"
		_, u := lowerSrc(t, src)
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an overridden logical operator")
		}
	})
}

// TestLowerIntLiterals pins two literal forms whose value the lowering must get
// right: a negative threshold (-1, represented as a unary neg over the literal)
// binds the signed value, and a leading-zero decimal (010) binds decimal 10, not
// octal 8 — the language reads a radix only from a 0b/0o/0x prefix.
func TestLowerIntLiterals(t *testing.T) {
	cases := []struct{ cond, sql, binds string }{
		{"self.id >= -1", `("id" >= ?)`, "[int -1]"},
		{"self.id == 010", `("id" = ?)`, "[int 10]"},
		{"self.id < -128", `("id" < ?)`, "[int -128]"},
	}
	for _, tc := range cases {
		got, u := lowerValidate(t, "id: int", tc.cond)
		if len(u) != 0 {
			t.Fatalf("%q: unsupported %+v", tc.cond, u)
		}
		if s := got.SQL(sql.SQLite); s != tc.sql {
			t.Errorf("%q: SQL = %q, want %q", tc.cond, s, tc.sql)
		}
		if b := bindsString(got.Binds()); b != tc.binds {
			t.Errorf("%q: binds = %s, want %s", tc.cond, b, tc.binds)
		}
	}
}

// TestLowerNullEitherSide pins that the null literal is handled on either side of
// the comparison — null == self.opt lowers the same as self.opt == null — so a
// valid predicate is not refused by operand order.
func TestLowerNullEitherSide(t *testing.T) {
	const fields = "id: int, opt: int | null"
	for _, cond := range []string{"self.opt == null", "null == self.opt"} {
		got, u := lowerValidate(t, fields, cond)
		if len(u) != 0 {
			t.Fatalf("%q: unsupported %+v", cond, u)
		}
		if s := got.SQL(sql.SQLite); s != `("opt" IS NULL)` {
			t.Errorf("%q: SQL = %q, want (\"opt\" IS NULL)", cond, s)
		}
	}
}
