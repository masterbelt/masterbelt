package sqlite_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/master"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// chainOf resolves a probe whose body is a relation query and returns the resolved
// chain value — the count()/where() method-call chain over the master relation that
// the driver evaluates.
func chainOf(t *testing.T, src, fileID, fnName string) ir.Value {
	t.Helper()
	prog := semantic.NewProgram()
	prog.SetFile(semantic.FileID(fileID), abstract.NewDocument([]byte(src)), nil)
	prog.Refresh()
	if diags := prog.Diagnostics(semantic.FileID(fileID)); len(diags) != 0 {
		t.Fatalf("query did not type-check: %v", diags)
	}
	m := prog.Module(semantic.FileID(fileID))
	for _, f := range m.Funcs {
		if f.Name != fnName {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return r.Value
			}
		}
	}
	t.Fatalf("no chain resolved for %q", fnName)
	return nil
}

// TestRelationDriverCount is the query driver's end-to-end proof: a relation query
// written in source (Cards.where(...).count(), and the unfiltered Cards.count()) is
// resolved to a chain, the driver recognizes it and lowers the where predicate to a
// Relation, and the engine — loaded with the master's data — runs it and returns
// the count. The whole query runs at compile time against real data.
func TestRelationDriverCount(t *testing.T) {
	const masterSrc = "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n"
	fields := []ir.Field{builtinField("id", "int"), builtinField("cost", "int")}
	table := master.Table{Columns: []string{"id", "cost"}, Rows: []master.Row{
		introw(2, 1, 10),
		introw(3, 2, 20),
		introw(4, 3, 40),
		introw(5, 4, 99),
	}}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)

	cases := []struct {
		name, body string
		want       int64
	}{
		{"unfiltered", "Cards.count()", 4},
		{"filtered", "Cards.where(fn(c) -> c.cost < 30).count()", 2},                                   // 10, 20
		{"filtered chain", "Cards.where(fn(c) -> c.cost < 30).where(fn(c) -> c.cost > 10).count()", 1}, // 20
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := masterSrc + "fn probe(): nint {\n  return " + tc.body + "\n}\n"
			chain := chainOf(t, src, "cards.belt", "probe")
			rel, m, unsupported, ok := mastersql.CountRelation(chain)
			if !ok {
				t.Fatalf("driver did not recognize the count chain")
			}
			if len(unsupported) != 0 {
				t.Fatalf("predicate did not lower: %+v", unsupported)
			}
			if m == nil || m.Name != "Cards" {
				t.Fatalf("chain master = %v, want Cards", m)
			}
			got, err := eng.Count(rel)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}
