package sqlite_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/master"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// chainOf resolves the probe function of a single-file source and returns the
// resolved chain value of its return — the count()/where() method-call chain over
// the master relation that the driver evaluates — together with the file's fold
// environment, which resolves the named constants a where predicate compares
// against.
func chainOf(t *testing.T, src string) (ir.Value, eval.GraphEnv) {
	t.Helper()
	const fileID = semantic.FileID("cards.belt")
	prog := semantic.NewProgram()
	prog.SetFile(fileID, abstract.NewDocument([]byte(src)), nil)
	prog.Refresh()
	if diags := prog.Diagnostics(fileID); len(diags) != 0 {
		t.Fatalf("query did not type-check: %v", diags)
	}
	env := prog.EvalEnv(fileID)
	m := prog.Module(fileID)
	for _, f := range m.Funcs {
		if f.Name != "probe" {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return r.Value, env
			}
		}
	}
	t.Fatal("no chain resolved for probe")
	return nil, nil
}

// TestRelationDriverRejectsBlockLambda pins that a where lambda with block control
// flow is not recognized rather than silently lowered to one branch's predicate: a
// conditional return means CountRelation reports ok=false, so the caller rejects the
// query instead of counting the wrong rows.
func TestRelationDriverRejectsBlockLambda(t *testing.T) {
	const src = "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.where(fn(c) {\n    if c.id > 0 {\n      return c.cost < 30\n    }\n    return c.cost > 10\n  }).count()\n}\n"
	chain, env := chainOf(t, src)
	if _, _, _, ok := mastersql.CountRelation(chain, env); ok {
		t.Fatal("a block-control-flow where lambda must not be recognized as a simple count chain")
	}
}

// TestRelationDriverFoldsConstOperands pins that a where predicate compared against
// a data-independent operand — a named constant or an arithmetic expression — runs:
// the driver folds the operand to a constant via the fold environment before
// lowering, since Lower binds only literals. The column comparison and the count
// stay; only the value side collapses. Each query runs against the loaded rows.
func TestRelationDriverFoldsConstOperands(t *testing.T) {
	const masterSrc = "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"const MIN_COST = 30\n"
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
		{"named constant", "Cards.where(fn(c) -> c.cost < MIN_COST).count()", 2},               // 10, 20
		{"arithmetic", "Cards.where(fn(c) -> c.cost < 50 + 49).count()", 3},                    // 10, 20, 40 (< 99)
		{"constant and arithmetic", "Cards.where(fn(c) -> c.cost < MIN_COST + 70).count()", 4}, // < 100
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := masterSrc + "fn probe(): nint {\n  return " + tc.body + "\n}\n"
			chain, env := chainOf(t, src)
			rel, m, unsupported, ok := mastersql.CountRelation(chain, env)
			if !ok {
				t.Fatalf("driver did not recognize the count chain")
			}
			if len(unsupported) != 0 {
				t.Fatalf("operand did not fold to a literal: %+v", unsupported)
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

// TestRelationDriverSeesThroughNestedAdapt pins that the driver recognizes a count
// chain wrapped in more than one Adapt — the shape write-back produces when the
// query's result is coerced through nested adaptations, e.g. returning it from a
// function declared with a union type (short | error): the count widens to short
// and then tags into the union, so the chain is Adapt(Adapt(count(...))). The
// recognizer must peel every Adapt; a single strip would leave an inner Adapt that
// hides the call and reject a valid type-checked query.
func TestRelationDriverSeesThroughNestedAdapt(t *testing.T) {
	const src = "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): short | error {\n  return Cards.where(fn(c) -> c.cost < 30).count()\n}\n"
	chain, env := chainOf(t, src)
	rel, m, unsupported, ok := mastersql.CountRelation(chain, env)
	if !ok {
		t.Fatal("the driver must see a count chain through nested Adapt wrappers")
	}
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	if m == nil || m.Name != "Cards" {
		t.Fatalf("chain master = %v, want Cards", m)
	}
	fields := []ir.Field{builtinField("id", "int"), builtinField("cost", "int")}
	table := master.Table{Columns: []string{"id", "cost"}, Rows: []master.Row{
		introw(2, 1, 10),
		introw(3, 2, 20),
		introw(4, 3, 40),
	}}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	got, err := eng.Count(rel)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != 2 { // cost 10, 20
		t.Errorf("count = %d, want 2", got)
	}
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
			chain, env := chainOf(t, src)
			rel, m, unsupported, ok := mastersql.CountRelation(chain, env)
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
