package sqlite_test

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/master/format/csv"
	"github.com/masterbelt/masterbelt/pkg/master/load"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// --- helpers for building a table and a predicate by hand --------------------

func builtinField(name, prim string) ir.Field {
	return ir.Field{Name: name, Type: &ir.Builtin{Name: prim}}
}

// columnDef stands in for the prelude's column<M, T>: the query lowering keys a
// column on its type's name, so a bare builtin def of that name with two
// parameters is enough for an engine test to build a column reference by hand.
var columnDef = &ir.TypeDef{Name: "column", Builtin: true, Params: []*ir.TypeParam{{Name: "M"}, {Name: "T"}}}

// colField builds a query column reference c.name of element type elem — the shape
// the query lowering recognizes: a field access whose type is column<M, elem>, read
// off the query binding rather than self.
func colField(name, elem string) *ir.FieldAccess {
	return &ir.FieldAccess{
		Receiver: &ir.ParamRef{Name: "c"},
		Field:    name,
		Type:     &ir.App{Def: columnDef, Args: []ir.Type{&ir.Named{Def: &ir.TypeDef{Name: "M"}}, &ir.Builtin{Name: elem}}},
	}
}

func call(method string, recv ir.Value, args ...ir.Value) *ir.Call {
	return &ir.Call{Method: method, Receiver: recv, Args: args}
}

// introw builds a row of integer cells, each origin on the given source line and
// in column order from 1, the shape the csv loader produces.
func introw(line int, vals ...int64) master.Row {
	cells := make([]master.Cell, len(vals))
	for i, v := range vals {
		cells[i] = master.Cell{Value: ir.IntConstant(big.NewInt(v)), Origin: master.Origin{Row: line, Col: i + 1}}
	}
	return master.Row{Cells: cells}
}

// closeEngine closes an engine and fails the test if it errors, so a deferred
// close still reports a leak rather than discarding it.
func closeEngine(t *testing.T, e *sqlite.Engine) {
	t.Helper()
	if err := e.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// rowIndexes lists the violations' row indexes, for comparing to an expected set.
func rowIndexes(vios []sqlite.Violation) []int {
	out := make([]int, len(vios))
	for i, v := range vios {
		out[i] = v.Row
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- focused engine tests ----------------------------------------------------

// TestViolationsFlagsFailingRows pins the engine's core: it loads the rows into
// :memory:, runs the lowered predicate, and returns the rows it does not hold for,
// in row order, each carrying the source origin a diagnostic anchors to.
func TestViolationsFlagsFailingRows(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int"), builtinField("power", "int"), builtinField("cost", "int")}
	table := master.Table{Columns: []string{"id", "power", "cost"}, Rows: []master.Row{
		introw(2, 1, 30, 10), // 30 >= 10, holds
		introw(3, 2, 5, 20),  // 5 >= 20, fails
		introw(4, 3, 40, 40), // 40 >= 40, holds
		introw(5, 4, 1, 99),  // 1 >= 99, fails
	}}
	pred, unsupported := mastersql.Lower(call("gteq", colField("power", "int"), colField("cost", "int")))
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(pred)
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if got := rowIndexes(vios); !equalInts(got, []int{1, 3}) {
		t.Fatalf("violating rows = %v, want [1 3]", got)
	}
	// Each violation anchors at its row's first cell — the locator the diagnostic
	// reports as path:row,col.
	if got := vios[0].Origin; got != (master.Origin{Row: 3, Col: 1}) {
		t.Errorf("first violation origin = %+v, want {Row:3 Col:1}", got)
	}
	if got := vios[1].Origin; got != (master.Origin{Row: 5, Col: 1}) {
		t.Errorf("second violation origin = %+v, want {Row:5 Col:1}", got)
	}
}

// TestViolationsNullIsFailSafe pins the three-valued rule: a predicate that is
// NULL for a row (a comparison against a NULL column) is not a pass — the row is
// flagged, the same fail-safe the per-row evaluator follows, so a missing value
// cannot slip a row past a check.
func TestViolationsNullIsFailSafe(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int"), builtinField("opt", "int")}
	table := master.Table{Columns: []string{"id", "opt"}, Rows: []master.Row{
		{Cells: []master.Cell{
			{Value: ir.IntConstant(big.NewInt(1)), Origin: master.Origin{Row: 2, Col: 1}},
			{Value: ir.IntConstant(big.NewInt(5)), Origin: master.Origin{Row: 2, Col: 2}},
		}}, // opt = 5, 5 >= 0 holds
		{Cells: []master.Cell{
			{Value: ir.IntConstant(big.NewInt(2)), Origin: master.Origin{Row: 3, Col: 1}},
			{Value: nil, Origin: master.Origin{Row: 3, Col: 2}},
		}}, // opt is NULL, NULL >= 0 is NULL, flagged
	}}
	pred, unsupported := mastersql.Lower(call("gteq", colField("opt", "int"), &ir.IntLiteral{Text: "0"}))
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(pred)
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if got := rowIndexes(vios); !equalInts(got, []int{1}) {
		t.Fatalf("violating rows = %v, want [1] (the NULL row)", got)
	}
}

// TestViolationsStringAndBool pins that the engine carries the non-integer scalars
// too: a string column compares as text and a bool column as the 0/1 it is stored
// as, so a predicate over them flags the right rows.
func TestViolationsStringAndBool(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int"), builtinField("name", "string"), builtinField("active", "bool")}
	row := func(line int, id int64, name string, active bool) master.Row {
		return master.Row{Cells: []master.Cell{
			{Value: ir.IntConstant(big.NewInt(id)), Origin: master.Origin{Row: line, Col: 1}},
			{Value: ir.StringConstant(name), Origin: master.Origin{Row: line, Col: 2}},
			{Value: ir.BoolConstant(active), Origin: master.Origin{Row: line, Col: 3}},
		}}
	}
	table := master.Table{Columns: []string{"id", "name", "active"}, Rows: []master.Row{
		row(2, 1, "fire", true),  // holds
		row(3, 2, "heal", true),  // name mismatch, fails
		row(4, 3, "fire", false), // active mismatch, fails
	}}
	cond := call("anan",
		call("eql", colField("name", "string"), &ir.StringLiteral{Value: "fire"}),
		call("eql", colField("active", "bool"), &ir.BoolLiteral{Value: true}),
	)
	pred, unsupported := mastersql.Lower(cond)
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(pred)
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if got := rowIndexes(vios); !equalInts(got, []int{1, 2}) {
		t.Fatalf("violating rows = %v, want [1 2]", got)
	}
}

// TestViolationsEmptyPredicate pins that a predicate the lowering produced nothing
// for holds for every row — the engine runs no query and reports no violation.
func TestViolationsEmptyPredicate(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int")}
	table := master.Table{Columns: []string{"id"}, Rows: []master.Row{introw(2, 1), introw(3, 2)}}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(mastersql.Predicate{})
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if len(vios) != 0 {
		t.Fatalf("violations = %v, want none for an empty predicate", vios)
	}
}

// TestLoadIntegerOutOfRange pins that an integer beyond SQLite's 64-bit storage is
// reported, not silently truncated to a wrong value the predicate would then judge
// against. Arbitrary-precision storage is a later concern.
func TestLoadIntegerOutOfRange(t *testing.T) {
	huge := new(big.Int).Lsh(big.NewInt(1), 70) // 2^70, well past int64
	table := master.Table{Columns: []string{"id"}, Rows: []master.Row{
		{Cells: []master.Cell{{Value: ir.IntConstant(huge), Origin: master.Origin{Row: 2, Col: 1}}}},
	}}
	if _, err := sqlite.Load([]ir.Field{builtinField("id", "int")}, table); err == nil {
		t.Fatal("Load accepted an integer outside SQLite's range; want an error")
	}
}

// TestViolationsWithRowidColumn pins that a master column named "rowid" does not
// corrupt the violation mapping: SQLite resolves an unquoted rowid to such a
// column, so the engine keys rows on a synthetic column it controls instead. The
// user "rowid" values here are non-sequential, so relying on them would map the
// violation to the wrong row.
func TestViolationsWithRowidColumn(t *testing.T) {
	fields := []ir.Field{builtinField("rowid", "int"), builtinField("val", "int")}
	table := master.Table{Columns: []string{"rowid", "val"}, Rows: []master.Row{
		introw(2, 100, 5),  // val 5 >= 0 holds
		introw(3, 200, -1), // val -1 >= 0 fails, row index 1
		introw(4, 300, 7),  // holds
	}}
	pred, unsupported := mastersql.Lower(call("gteq", colField("val", "int"), &ir.IntLiteral{Text: "0"}))
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(pred)
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}
	if got := rowIndexes(vios); !equalInts(got, []int{1}) {
		t.Fatalf("violating rows = %v, want [1]", got)
	}
	if got := vios[0].Origin; got != (master.Origin{Row: 3, Col: 1}) {
		t.Errorf("violation origin = %+v, want {Row:3 Col:1}", got)
	}
}

// TestViolationsConcurrent pins that the in-memory database is reachable from
// overlapping queries: with :memory: each connection is a private database, so the
// pool must be held to one connection or a query can run on a connection where the
// table was never created and fail with "no such table". Many goroutines fire at
// once to force a second connection if the pool is not pinned.
func TestViolationsConcurrent(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int"), builtinField("power", "int")}
	table := master.Table{Columns: []string{"id", "power"}, Rows: []master.Row{
		introw(2, 1, 5),
		introw(3, 2, -1), // fails power >= 0, row index 1
	}}
	pred, unsupported := mastersql.Lower(call("gteq", colField("power", "int"), &ir.IntLiteral{Text: "0"}))
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // fire together, maximizing the chance of a second connection
			vios, err := eng.Violations(pred)
			if err != nil {
				errs <- err
				return
			}
			if got := rowIndexes(vios); !equalInts(got, []int{1}) {
				errs <- fmt.Errorf("violating rows = %v, want [1]", got)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Violations: %v", err)
	}
}

// TestCountAllAndFiltered pins the scalar count path: an unfiltered relation
// counts every loaded row, and a filtered one counts the rows its predicate is
// true for (a row the predicate is false or null for is not counted — the
// matching-count semantics, distinct from a validation's fail-safe).
func TestCountAllAndFiltered(t *testing.T) {
	fields := []ir.Field{builtinField("id", "int"), builtinField("power", "int")}
	table := master.Table{Columns: []string{"id", "power"}, Rows: []master.Row{
		introw(2, 1, 5),
		introw(3, 2, 0),
		introw(4, 3, 30),
		introw(5, 4, -7),
	}}
	eng, err := sqlite.Load(fields, table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)

	all, err := eng.Count(mastersql.All())
	if err != nil {
		t.Fatalf("Count(all): %v", err)
	}
	if all != 4 {
		t.Errorf("count(all) = %d, want 4", all)
	}

	pred, unsupported := mastersql.Lower(call("gt", colField("power", "int"), &ir.IntLiteral{Text: "0"}))
	if len(unsupported) != 0 {
		t.Fatalf("predicate did not lower: %+v", unsupported)
	}
	positive, err := eng.Count(mastersql.All().Where(pred))
	if err != nil {
		t.Fatalf("Count(where): %v", err)
	}
	if positive != 2 {
		t.Errorf("count(power > 0) = %d, want 2 (powers 5 and 30)", positive)
	}

	// Narrowing again intersects: power > 0 AND power < 10 keeps only the row with
	// power 5, not every row matching the second filter alone.
	small, lowered := mastersql.Lower(call("lt", colField("power", "int"), &ir.IntLiteral{Text: "10"}))
	if len(lowered) != 0 {
		t.Fatalf("predicate did not lower: %+v", lowered)
	}
	both, err := eng.Count(mastersql.All().Where(pred).Where(small))
	if err != nil {
		t.Fatalf("Count(where.where): %v", err)
	}
	if both != 1 {
		t.Errorf("count(power > 0 AND power < 10) = %d, want 1 (power 5)", both)
	}
}

// --- end-to-end fixture: a real project loaded and validated -----------------

// TestEngineOnProjectFixture is the canonical proof: a project of a .belt master
// and its .csv data is read through the real load path, a query condition over the
// master's columns (a predicate<M> from the query binding) is lowered to SQL, and
// the engine flags exactly the rows that fail it — the same rows the per-row
// evaluator independently reports for the equivalent value-mode validate. It proves
// the column-mode SQL path and the value-mode evaluator agree on real data: the
// query says c.power >= c.cost, the validate says self.power >= self.cost, and both
// flag the same rows.
func TestEngineOnProjectFixture(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "data/skills.csv", "id,power,cost\n1,30,10\n2,5,20\n3,40,40\n4,1,99\n")
	belt := "master Skill {\n" +
		"  record { id: int, power: int, cost: int }\n" +
		"  primary id\n" +
		"  validate {\n    each {\n      assert self.power >= self.cost\n    }\n  }\n" +
		"  source { csv \"skills.csv\" }\n" +
		"}\n" +
		"fn strong(c: columns<Skill>): predicate<Skill> {\n  return c.power >= c.cost\n}\n"

	prog := semantic.NewProgram()
	prog.SetFile("skills.belt", abstract.NewDocument([]byte(belt)), nil)
	prog.Refresh()

	reg := master.NewRegistry()
	reg.Register(csv.New())
	loaded, diags := load.File(prog, "skills.belt", root, map[string]string{"csv": "data"}, reg)
	if len(loaded) != 1 {
		t.Fatalf("loaded %d tables, want 1", len(loaded))
	}

	def := masterDef(t, prog)
	fields, ok := master.RowFields(def.Master.Row)
	if !ok {
		t.Fatal("row fields did not resolve")
	}
	pred, unsupported := mastersql.Lower(probePredicate(t, prog, "strong"))
	if len(unsupported) != 0 {
		t.Fatalf("the query condition did not lower: %+v", unsupported)
	}

	eng, err := sqlite.Load(fields, loaded[0].Table)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer closeEngine(t, eng)
	vios, err := eng.Violations(pred)
	if err != nil {
		t.Fatalf("Violations: %v", err)
	}

	// Rows 2 (power 5 < cost 20) and 4 (power 1 < cost 99) fail — indexes 1 and 3,
	// on source lines 3 and 5 (line 1 is the header).
	if got := rowIndexes(vios); !equalInts(got, []int{1, 3}) {
		t.Fatalf("engine flagged rows %v, want [1 3]", got)
	}
	if vios[0].Origin.Row != 3 || vios[1].Origin.Row != 5 {
		t.Errorf("violation origins on lines %d and %d, want 3 and 5", vios[0].Origin.Row, vios[1].Origin.Row)
	}

	// The same relation counts over the loaded data: 4 rows in all, and 2 satisfy
	// power >= cost (the complement of the 2 violations the predicate flags).
	if total, err := eng.Count(mastersql.All()); err != nil || total != 4 {
		t.Errorf("Count(all) = %d (err %v), want 4", total, err)
	}
	if pass, err := eng.Count(mastersql.All().Where(pred)); err != nil || pass != 2 {
		t.Errorf("Count(power >= cost) = %d (err %v), want 2", pass, err)
	}

	// The per-row evaluator, run by the loader, independently reports the same two
	// rows — the engine and the evaluator agree on real data.
	if got := countRowValidationFailures(diags); got != len(vios) {
		t.Errorf("loader reported %d row-validation failures, engine found %d; they must agree", got, len(vios))
	}
}

// probePredicate returns the resolved query condition a probe function yields — the
// predicate<M> value graph of fn name(c: columns<M>): predicate<M> { return cond } —
// the real graph the query pipeline lowers.
func probePredicate(t *testing.T, prog *semantic.Program, name string) ir.Value {
	t.Helper()
	module := prog.Module("skills.belt")
	if module == nil {
		t.Fatal("no module for skills.belt")
	}
	for _, f := range module.Funcs {
		if f.Name != name {
			continue
		}
		for _, s := range f.Body {
			if r, ok := s.(*ir.Return); ok && r.Value != nil {
				return r.Value
			}
		}
	}
	t.Fatalf("no predicate resolved for probe %q", name)
	return nil
}

func masterDef(t *testing.T, prog *semantic.Program) *ir.TypeDef {
	t.Helper()
	module := prog.Module("skills.belt")
	if module == nil {
		t.Fatal("no module for skills.belt")
	}
	for _, def := range module.Types {
		if def.Master != nil {
			return def
		}
	}
	t.Fatal("no master in module")
	return nil
}

func countRowValidationFailures(diags []diagnostic.Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Code == master.CodeRowValidationFailed {
			n++
		}
	}
	return n
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
