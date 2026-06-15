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

// lowerProbe resolves a query condition over a master and lowers it to SQL. The
// condition is the body of a probe — fn probe(c: columns<M>): predicate<M> { return
// cond } — so c.field reads a typed column and the comparison settles to the
// predicate<M> the query pipeline lowers, the real value graph rather than a
// hand-built one. preamble is any extra declarations the condition needs (an enum,
// a column's nominal type); fields is the master's record body.
func lowerProbe(t *testing.T, preamble, fields, cond string) (sql.Predicate, []sql.Unsupported) {
	t.Helper()
	src := preamble +
		"master M {\n  record { " + fields + " }\n  primary id\n}\n" +
		"fn probe(c: columns<M>): predicate<M> {\n  return " + cond + "\n}\n"
	prog := semantic.NewProgram()
	prog.SetFile("m.belt", abstract.NewDocument([]byte(src)), nil)
	prog.Refresh()
	// The condition must type-check before its lowering means anything: a probe
	// that resolves with diagnostics is rejected query source, so lowering its graph
	// would pin SQL for a predicate the analyzer never accepts.
	if diags := prog.Diagnostics("m.belt"); len(diags) != 0 {
		t.Fatalf("probe %q did not type-check: %v", cond, diags)
	}
	m := prog.Module("m.belt")
	if m == nil {
		t.Fatalf("no module for %q", cond)
	}
	for _, f := range m.Funcs {
		if f.Name != "probe" {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return sql.Lower(r.Value)
			}
		}
	}
	t.Fatalf("no probe predicate resolved for %q", cond)
	return sql.Predicate{}, nil
}

// lowerCond is lowerProbe with the common record body (id/power/cost/name/active)
// and no preamble.
func lowerCond(t *testing.T, cond string) (sql.Predicate, []sql.Unsupported) {
	t.Helper()
	return lowerProbe(t, "", "id: int, power: int, cost: int, name: string, active: bool", cond)
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

// TestLowerCore pins the core query-condition vocabulary lowering to single-table
// SQL: column comparisons (against a value or another column), the logical
// operators, negation, and literals (parameterized), with each generated fragment
// and its binds fixed as a golden.
func TestLowerCore(t *testing.T) {
	cases := []struct {
		name, cond, sql, binds string
	}{
		{"column vs column", "c.power >= c.cost", `("power" >= "cost")`, "[]"},
		{"column vs int", "c.power >= 1", `("power" >= ?)`, "[int 1]"},
		{"equality", "c.power == 0", `("power" = ?)`, "[int 0]"},
		{"inequality", "c.cost != 2", `("cost" <> ?)`, "[int 2]"},
		{"less / greater", "c.power < 10", `("power" < ?)`, "[int 10]"},
		{"string equality", "c.name == \"fire\"", `("name" = ?)`, `[text "fire"]`},
		{"bool equality", "c.active == true", `("active" = ?)`, "[bool true]"},
		{"logical and", "c.power >= 1 && c.cost != 2", `(("power" >= ?) AND ("cost" <> ?))`, "[int 1, int 2]"},
		{"logical or", "c.power == 0 || c.cost == 0", `(("power" = ?) OR ("cost" = ?))`, "[int 0, int 0]"},
		{"negation", "!(c.power >= 1)", `(NOT ("power" >= ?))`, "[int 1]"},
		{"nested", "c.power >= 1 && (c.cost == 0 || c.cost == 2)", `(("power" >= ?) AND (("cost" = ?) OR ("cost" = ?)))`, "[int 1, int 0, int 2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unsupported := lowerCond(t, tc.cond)
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

// TestLowerEnumColumn pins that a comparison against an enum member lowers to a
// bound comparison on the member's underlying base value — the value stored for the
// row — so a query over an enum column renders to SQL like any other. Both the
// qualified member (Rarity.legend) and the bare member (legend, resolved through
// the column's element type) lower to the same bound.
func TestLowerEnumColumn(t *testing.T) {
	const preamble = "enum Rarity { common; rare; legend }\n"
	// common=0, rare=1, legend=2: the bound value is the member's base integer.
	for _, member := range []string{"Rarity.legend", "legend"} {
		got, u := lowerProbe(t, preamble, "id: int, rarity: Rarity", "c.rarity == "+member)
		if len(u) != 0 {
			t.Fatalf("%s: unsupported: %+v", member, u)
		}
		if s := got.SQL(sql.SQLite); s != `("rarity" = ?)` {
			t.Errorf("%s: SQL = %q, want (\"rarity\" = ?)", member, s)
		}
		if b := bindsString(got.Binds()); b != "[int 2]" {
			t.Errorf("%s: binds = %s, want [int 2]", member, b)
		}
	}
}

// TestLowerNull pins that equality and inequality against null become IS NULL and
// IS NOT NULL, with no bind — SQL's = NULL is never true.
func TestLowerNull(t *testing.T) {
	t.Run("is null", func(t *testing.T) {
		got, u := lowerProbe(t, "", "id: int, opt: int | null", "c.opt == null")
		if len(u) != 0 {
			t.Fatalf("unsupported: %+v", u)
		}
		if s := got.SQL(sql.SQLite); s != `("opt" IS NULL)` || len(got.Binds()) != 0 {
			t.Errorf("got %q binds %s, want (\"opt\" IS NULL) []", s, bindsString(got.Binds()))
		}
	})
	t.Run("is not null", func(t *testing.T) {
		got, u := lowerProbe(t, "", "id: int, opt: int | null", "c.opt != null")
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
	got, u := lowerProbe(t, "", "id: int, power: int, cost: int", "c.power >= 1 && c.cost != 2")
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

// TestLowerUnsupported pins that a query condition outside the core is reported as
// Unsupported rather than silently dropped or mis-lowered, so the caller rejects
// it. The typed algebra guarantees a predicate<M> is SQL-expressible by
// construction — except a column whose element type overrides the comparison
// operator, which does not carry the builtin's SQL semantics and so cannot be
// emitted as a plain =.
func TestLowerUnsupported(t *testing.T) {
	t.Run("overridden operator", func(t *testing.T) {
		// A column whose type overrides the comparison operator does not carry the
		// builtin's SQL semantics, so the comparison is rejected, not emitted as =.
		preamble := "type Weird = int impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n"
		_, u := lowerProbe(t, preamble, "id: int, w: Weird", "c.w == c.w")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an overridden operator")
		}
	})
	t.Run("overridden operator on a generic type", func(t *testing.T) {
		// The override check must see through a generic application (Weird<string>
		// is an applied nominal, not a bare one), or the custom operator slips past.
		preamble := "type Weird<T> = int impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n"
		_, u := lowerProbe(t, preamble, "id: int, w: Weird<string>", "c.w == c.w")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for an overridden operator on a generic type")
		}
	})
	t.Run("custom comparison on a nullable column", func(t *testing.T) {
		// A nullable custom-comparison column (Weird | null) must still be rejected:
		// the override check unwraps the null to find Weird's own eql, rather than
		// seeing the non-nominal union and emitting plain SQL equality.
		preamble := "type Weird = int impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n"
		_, u := lowerProbe(t, preamble, "id: int, w: Weird | null", "c.w == c.w")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for a nullable custom-comparison column")
		}
	})
	t.Run("custom comparison on an enum", func(t *testing.T) {
		// An enum's synthesized comparisons lower (they compare the base value), but
		// a comparison the enum's own impl defines is user logic SQL cannot stand in
		// for, so it is rejected like any other overridden operator.
		preamble := "enum R { a; b } impl {\n  pub eql(other: self): bool {\n    return false\n  }\n}\n"
		_, u := lowerProbe(t, preamble, "id: int, r: R", "c.r == c.r")
		if len(u) == 0 {
			t.Fatal("want an unsupported node for a custom comparison declared on an enum")
		}
	})
}

// TestLowerIntLiterals pins two literal forms whose value the lowering must get
// right: a negative threshold (-1, represented as a unary neg over the literal)
// binds the signed value, and a leading-zero decimal (010) binds decimal 10, not
// octal 8 — the language reads a radix only from a 0b/0o/0x prefix.
func TestLowerIntLiterals(t *testing.T) {
	cases := []struct{ cond, sql, binds string }{
		{"c.id >= -1", `("id" >= ?)`, "[int -1]"},
		{"c.id == 010", `("id" = ?)`, "[int 10]"},
		{"c.id < -128", `("id" < ?)`, "[int -128]"},
	}
	for _, tc := range cases {
		got, u := lowerProbe(t, "", "id: int", tc.cond)
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

// nullCol is a column<M, int> reference, built by hand for the lowering-level
// null tests. Analyzed query source always places the column on the left (infix
// desugars the left operand to the receiver, and null has no query method), so
// the right-side form is exercised through a hand-built graph rather than source.
var nullColDef = &ir.TypeDef{Name: "column", Builtin: true, Params: []*ir.TypeParam{{Name: "M"}, {Name: "T"}}}

func nullCol(name string) *ir.FieldAccess {
	return &ir.FieldAccess{
		Receiver: &ir.ParamRef{Name: "c"},
		Field:    name,
		Type:     &ir.App{Def: nullColDef, Args: []ir.Type{&ir.Named{Def: &ir.TypeDef{Name: "M"}}, &ir.Builtin{Name: "nint"}}},
	}
}

// TestLowerNullEitherSide pins that the lowering produces IS NULL whether the null
// literal is the comparison's argument (the form analyzed source takes, c.opt ==
// null) or its receiver (null == c.opt) — so the lowering stays robust to operand
// order even though the checker only admits the column-on-left form. The graphs are
// built by hand because the receiver form is rejected before lowering.
func TestLowerNullEitherSide(t *testing.T) {
	graphs := map[string]*ir.Call{
		"null as argument": {Method: "eql", Receiver: nullCol("opt"), Args: []ir.Value{&ir.NullValue{}}},
		"null as receiver": {Method: "eql", Receiver: &ir.NullValue{}, Args: []ir.Value{nullCol("opt")}},
	}
	for name, g := range graphs {
		got, u := sql.Lower(g)
		if len(u) != 0 {
			t.Fatalf("%s: unsupported %+v", name, u)
		}
		if s := got.SQL(sql.SQLite); s != `("opt" IS NULL)` {
			t.Errorf("%s: SQL = %q, want (\"opt\" IS NULL)", name, s)
		}
	}
}
