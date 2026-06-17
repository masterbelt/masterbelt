package load_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/master/format/csv"
	"github.com/masterbelt/masterbelt/pkg/master/load"
)

// skillBelt is a master with a refined field; the source's base path is supplied
// by the caller, not the manifest, so the loader is exercised without a project.
const skillBelt = "type Level = int where self > 0\n\n" +
	"master Skill {\n" +
	"  record { id: int, name: string, power: Level }\n" +
	"  primary id\n" +
	"  source { csv \"skills.csv\" }\n" +
	"}\n"

// run analyzes beltSrc as one file, writes each data file under a temp root, and
// loads the master data with csv registered and the given per-format base paths.
func run(t *testing.T, beltSrc string, bases map[string]string, files map[string]string) ([]load.Loaded, []diagnostic.Diagnostic) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prog := semantic.NewProgram()
	prog.SetFile("skills.belt", abstract.NewDocument([]byte(beltSrc)), nil)
	prog.Refresh()
	reg := master.NewRegistry()
	reg.Register(csv.New())
	return load.File(prog, "skills.belt", root, bases, reg)
}

// countTableFailures counts the per-table validate failures among the diagnostics.
func countTableFailures(diags []diagnostic.Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Code == master.CodeTableValidationFailed {
			n++
		}
	}
	return n
}

// TestValidateAllRowCountCap exercises a per-table validate all check end to end:
// the count of loaded rows is evaluated in the in-memory SQLite engine and
// compared to the cap. Under the cap the table passes; at or over it the check
// fails once, anchored at the assert.
func TestValidateAllRowCountCap(t *testing.T) {
	const belt = "master Item {\n" +
		"  record { id: int, power: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert count < 3\n    }\n  }\n" +
		"  source { csv \"items.csv\" }\n" +
		"}\n"
	bases := map[string]string{"csv": "data"}

	// Two rows: count 2 < 3, the table passes.
	if _, diags := run(t, belt, bases, map[string]string{
		"data/items.csv": "id,power\n1,10\n2,20\n",
	}); countTableFailures(diags) != 0 {
		t.Errorf("2 rows: table_validation_failed = %d, want 0 (count 2 < 3)", countTableFailures(diags))
	}

	// Three rows: count 3 is not < 3, the table fails once.
	if _, diags := run(t, belt, bases, map[string]string{
		"data/items.csv": "id,power\n1,10\n2,20\n3,30\n",
	}); countTableFailures(diags) != 1 {
		t.Errorf("3 rows: table_validation_failed = %d, want 1 (count 3 not < 3)", countTableFailures(diags))
	}
}

// TestValidateAllCountIgnoresCellValues pins that a whole-table count does not
// depend on cell values: a row whose nint value is outside SQLite's int64 range is
// still counted, so `count < 1` fails for it. Regression: counting by loading every
// cell into SQLite let an out-of-range value fail the load and silently skip the
// check, passing a file that should fail.
func TestValidateAllCountIgnoresCellValues(t *testing.T) {
	const belt = "master Big {\n" +
		"  record { id: int, n: nint }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert count < 1\n    }\n  }\n" +
		"  source { csv \"big.csv\" }\n" +
		"}\n"
	// One row with a value far past int64: it is counted (count 1), so count < 1 fails.
	if _, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/big.csv": "id,n\n1,999999999999999999999999999\n",
	}); countTableFailures(diags) != 1 {
		t.Errorf("table_validation_failed = %d, want 1 (count 1 not < 1, despite the out-of-range cell)", countTableFailures(diags))
	}
}

// TestValidateAllCountInClosure pins that count keeps its value inside a closure
// in a validate all check: the relation count is carried through the applied
// function the same way self is. Regression: graphApply built a fresh context
// without the count, so count folded to nil and the check failed regardless of the
// actual row count.
func TestValidateAllCountInClosure(t *testing.T) {
	const belt = "master Item {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert (fn(): bool { return count < 3 })()\n    }\n  }\n" +
		"  source { csv \"items.csv\" }\n" +
		"}\n"
	if _, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/items.csv": "id\n1\n2\n",
	}); countTableFailures(diags) != 0 {
		t.Errorf("table_validation_failed = %d, want 0 (count 2 < 3 inside a closure)", countTableFailures(diags))
	}
}

// TestValidateAllCountThroughHelper pins that count keeps its value when a closure
// that references it is invoked through a helper function. Regression: the helper
// application built a fresh fold context without the relation count, so count
// folded to nil through the helper and the check failed despite the row count.
func TestValidateAllCountThroughHelper(t *testing.T) {
	const belt = "fn apply(f: fn(): bool): bool {\n  return f()\n}\n\n" +
		"master Item {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert apply(fn(): bool { return count < 3 })\n    }\n  }\n" +
		"  source { csv \"items.csv\" }\n" +
		"}\n"
	if _, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/items.csv": "id\n1\n2\n",
	}); countTableFailures(diags) != 0 {
		t.Errorf("table_validation_failed = %d, want 0 (count 2 < 3 through a helper)", countTableFailures(diags))
	}
}

// TestValidateAllStaticRelationAggregate pins the let-bearing relation path: a
// master static fn reads self as the relation, narrows it through a let-bound
// where, and aggregates over the binding (sum over count). A per-table check that
// calls it folds against the loaded rows — the driver runs the where-narrowed sum
// and count in the engine and the arithmetic folds over the results. costs 10 and
// 30 give sum 40 over count 2, an average of 20.
func TestValidateAllStaticRelationAggregate(t *testing.T) {
	mk := func(cmp string) string {
		return "master Cards {\n" +
			"  record { id: int, cost: int } impl {\n" +
			"    pub static fn avg_cost(): nint {\n" +
			"      let m = self.where(fn(c) -> c.cost > 0)\n" +
			"      return m.sum(fn(c) -> c.cost) / m.count()\n" +
			"    }\n  }\n" +
			"  primary id\n" +
			"  validate {\n    all {\n      assert Cards.avg_cost() " + cmp + "\n    }\n  }\n" +
			"  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n"}

	if _, diags := run(t, mk("== 20"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("avg_cost == 20: table_validation_failed = %d, want 0 (sum 40 over count 2 = 20)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("== 99"), bases, data); countTableFailures(diags) != 1 {
		t.Errorf("avg_cost == 99: table_validation_failed = %d, want 1 (the average is 20, not 99)", countTableFailures(diags))
	}
}

// TestValidateAllStaticRelationCrossMaster pins that a static fn returning another
// master's relation is not folded against this master's engine: A's check calls a fn
// returning B.count(), and the engine holds only A's rows, so driving it there would
// answer with A's count. The cross-master query is left undriven and the check fails
// safe (B has 2 rows, so == 1 is false either way — the point is it is not wrongly
// passed by counting A's single row).
func TestValidateAllStaticRelationCrossMaster(t *testing.T) {
	const belt = "master B {\n  record { id: int }\n  primary id\n  source { csv \"b.csv\" }\n}\n" +
		"master A {\n  record { id: int } impl {\n    pub static fn bcount(): nint {\n      return B.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert A.bcount() == 1\n    }\n  }\n  source { csv \"a.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/a.csv": "id\n1\n", "data/b.csv": "id\n1\n2\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("cross-master A.bcount() == 1: table_validation_failed = %d, want 1 (B has 2 rows; A's count must not pass it)", countTableFailures(diags))
	}
}

// TestValidateAllStaticRelationResultOverflow pins that a static fn whose declared
// result type cannot hold the aggregate is not folded to an in-range value: a sum of
// 300 does not inhabit sbyte, so Cards.s() must not be driven to 300 and pass — the
// overflowing result leaves the check undriven and it fails safe.
func TestValidateAllStaticRelationResultOverflow(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn s(): sbyte {\n      return self.sum(fn(c) -> c.cost)\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.s() == 300\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("sbyte overflow Cards.s() == 300: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
	}
}

// TestValidateAllStaticRelationThroughHelper pins that a relation query reached
// through a top-level helper call is still driven: eq(Cards.size(), 2) drives the
// argument (the row count) so the helper folds, rather than leaving the bare relation
// for the evaluator to choke on.
func TestValidateAllStaticRelationThroughHelper(t *testing.T) {
	const belt = "fn eq(a: nint, b: nint): bool {\n  return a == b\n}\n" +
		"master Cards {\n  record { id: int } impl {\n    pub static fn size(): nint {\n      return self.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert eq(Cards.size(), 2)\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("eq(Cards.size(), 2) over 2 rows: table_validation_failed = %d, want 0 (the count drives through the helper)", countTableFailures(diags))
	}
}

// TestValidateAllStaticScalarBody pins that a master static fn with no relation
// query keeps its ordinary fold: a scalar let and arithmetic (let x = 3; return
// x + 1) is left to the evaluator, not broken by the relation driver dropping the
// binding.
func TestValidateAllStaticScalarBody(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn f(): nint {\n      let x = 3\n      return x + 1\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 4\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("scalar Cards.f() == 4: table_validation_failed = %d, want 0 (let x = 3; return x + 1)", countTableFailures(diags))
	}
}

// TestValidateAllStaticUnfilteredCountIgnoresCells pins that a static fn's unfiltered
// count reads the row count, not the loaded cells: an out-of-range nint cell fails
// the engine load, but Cards.size() (self.count(), no where) still counts the one row
// so == 1 passes — the same independence the bare count check has.
func TestValidateAllStaticUnfilteredCountIgnoresCells(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, n: nint } impl {\n    pub static fn size(): nint {\n      return self.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.size() == 1\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,n\n1,999999999999999999999999999\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("unfiltered Cards.size() == 1 with an out-of-range cell: table_validation_failed = %d, want 0 (count reads the row count)", countTableFailures(diags))
	}
}

// TestValidateAllStaticScalarLetAroundAggregate pins that a scalar local around a
// relation aggregate folds: let one = 1; return self.count() + one is the row count
// plus one, the evaluator binding the scalar and the relation folder running the
// count. Over two rows, sizePlus() == 3 passes.
func TestValidateAllStaticScalarLetAroundAggregate(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn sizePlus(): nint {\n      let one = 1\n      return self.count() + one\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.sizePlus() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("sizePlus() == 3 over 2 rows: table_validation_failed = %d, want 0 (count 2 + 1)", countTableFailures(diags))
	}
}

// TestValidateAllStaticDelegation pins that a static fn delegating to another that
// queries the relation folds: size2() returns Cards.size(), which returns
// self.count(); the evaluator folds the delegation and the inner count runs.
func TestValidateAllStaticDelegation(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n" +
		"    pub static fn size(): nint {\n      return self.count()\n    }\n" +
		"    pub static fn size2(): nint {\n      return Cards.size()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.size2() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("Cards.size2() == 2 (delegating to size to count): table_validation_failed = %d, want 0", countTableFailures(diags))
	}
}

// TestValidateAllRelationInConditional pins that a relation aggregate inside a
// conditional folds: the evaluator takes the live branch and the relation folder
// runs the count there.
func TestValidateAllRelationInConditional(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn size(): nint {\n      return self.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert true ? Cards.size() == 2 : false\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("ternary over Cards.size() == 2: table_validation_failed = %d, want 0", countTableFailures(diags))
	}
}

// TestValidateAllStaticRefinedResultViolation pins that a static fn whose driven
// aggregate violates its refined result type's predicate is not folded to a passing
// value: a sum of 0 does not satisfy Positive (self > 0), so Cards.s() must not be
// driven to 0 and pass — the violating result leaves the check undriven and it fails
// safe. types.Fits alone would miss this; the refinement predicate is evaluated.
func TestValidateAllStaticRefinedResultViolation(t *testing.T) {
	const belt = "type Positive = int where self > 0\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn s(): Positive {\n      return self.sum(fn(c) -> c.cost)\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.s() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,0\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("Positive Cards.s() == 0: table_validation_failed = %d, want 1 (0 violates self > 0)", countTableFailures(diags))
	}
}

// TestValidateAllRelationLetShadowedInBlock pins that a relation local shadowed by
// a nested block does not leak past the block: the inner block rebinds m to a
// narrower filter, but after the block the outer m must still be the filter it was
// bound to. Over ids 5, 15, 25, the outer m (id > 0) counts all three while a leaked
// inner m (id > 10) would count two, so == 3 passes only when the block's binding is
// restored on exit.
func TestValidateAllRelationLetShadowedInBlock(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn outer(): nint {\n" +
		"      let m = self.where(fn(c) -> c.id > 0)\n" +
		"      if true {\n        let m = self.where(fn(c) -> c.id > 10)\n      }\n" +
		"      return m.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.outer() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n5\n15\n25\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("shadowed relation let Cards.outer() == 3: table_validation_failed = %d, want 0 (the outer m counts all three; the inner block must not leak)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIntoNarrowParam pins that a relation aggregate flowing
// into a non-union sized position the analyzer could not check — a typed parameter —
// is range-checked against the rows' value: a sum of 300 does not inhabit sbyte, so
// fits(Cards.sum(...)) must not pass on a 300 smuggled past the annotation. The
// overflowing argument leaves the call unfoldable and the check fails safe.
func TestValidateAllAggregateIntoNarrowParam(t *testing.T) {
	const belt = "fn fits(x: sbyte): bool {\n  return true\n}\n" +
		"master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert fits(Cards.sum(fn(c) -> c.cost))\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("fits(Cards.sum) with sum 300 into sbyte: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIntoNarrowLet pins the same range check at an annotated
// let: let x: sbyte = self.sum(...) over a sum of 300 must not bind 300, so the body
// is left unfoldable and the check fails safe rather than reading the out-of-range
// value back as if it fit.
func TestValidateAllAggregateIntoNarrowLet(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n" +
		"      let x: sbyte = self.sum(fn(c) -> c.cost)\n      return x >= 0\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("let x: sbyte = sum 300: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIgnoresUnrelatedWideColumn pins that a column holding an
// integer beyond SQLite's range does not disable an aggregate over the other
// columns: sum(cost) is driven and passes despite a wide big cell, while a query that
// reaches the dropped column itself (sum(big)) finds no such column and fails safe.
func TestValidateAllAggregateIgnoresUnrelatedWideColumn(t *testing.T) {
	mk := func(sel, cmp string) string {
		return "master Cards {\n  record { id: int, cost: int, big: nint }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.sum(fn(c) -> c." + sel + ") " + cmp + "\n    }\n  }\n" +
			"  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost,big\n1,10,999999999999999999999999999\n2,20,1\n"}
	if _, diags := run(t, mk("cost", "== 30"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("sum(cost) == 30 with a wide big cell: table_validation_failed = %d, want 0 (the unrelated wide column must not disable the aggregate)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("big", "== 0"), bases, data); countTableFailures(diags) != 1 {
		t.Errorf("sum(big) == 0 over the dropped column: table_validation_failed = %d, want 1 (a query reaching the wide column fails safe)", countTableFailures(diags))
	}
}

func TestLoadTypedRows(t *testing.T) {
	loaded, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\n1,Fireball,30\n2,Heal,12\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %d tables, want 1", len(loaded))
	}
	got := loaded[0]
	if got.Master != "Skill" || got.Display != "data/skills.csv" {
		t.Errorf("loaded = %+v, want Skill <- data/skills.csv", got)
	}
	if s := got.Table.String(); s != "id, name, power\n1 | \"Fireball\" | 30\n2 | \"Heal\" | 12\n" {
		t.Errorf("table =\n%q", s)
	}
}

func TestLoadCellTypeMismatch(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\noops,Heal,12\n",
	})
	d := single(t, diags, master.CodeCellTypeMismatch)
	for _, frag := range []string{"data/skills.csv:2,1", "oops", "int", "id"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

func TestLoadRefinementViolation(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\n1,Drain,-5\n",
	})
	d := single(t, diags, master.CodeCellRefinement)
	for _, frag := range []string{"data/skills.csv:2,9", "-5", "Level"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

// validateBelt is a master whose per-row validate each checks compare two
// columns of the row through self — the row predicate the evaluator folds
// against every loaded row.
const validateBelt = "master Skill {\n" +
	"  record { id: int, cost: int, power: int }\n" +
	"  primary id\n" +
	"  source { csv \"skills.csv\" }\n" +
	"  validate {\n" +
	"    each {\n" +
	"      assert self.power >= self.cost\n" +
	"      assert self.id > 0\n" +
	"    }\n" +
	"  }\n" +
	"}\n"

func TestLoadRowValidationFailed(t *testing.T) {
	// The second row's power (20) is below its cost (50), so its per-row check
	// fails; the diagnostic names the failing data row as path:row.
	_, diags := run(t, validateBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost,power\n1,10,30\n2,50,20\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want it to name the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationClean(t *testing.T) {
	// Every row satisfies both checks, so the loader reports nothing.
	_, diags := run(t, validateBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost,power\n1,10,30\n2,5,20\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
}

func TestLoadRowValidationSkippedOnMissingColumn(t *testing.T) {
	// The source has no power column, which a check reads. The missing column is
	// reported once; the validation does not run, so no derived row-validation
	// error is piled on every row.
	_, diags := run(t, validateBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost\n1,10\n2,20\n",
	})
	d := single(t, diags, master.CodeMissingColumn)
	if !strings.Contains(d.Message, "power") {
		t.Errorf("message = %q, want the missing power column", d.Message)
	}
}

func TestLoadRowValidationSkippedAfterRefinementFailure(t *testing.T) {
	// A row whose cell fails its field refinement is reported once
	// (cell_refinement); the per-row check is not run over the value the row type
	// already rejected, so no derived row_validation_failed piles onto it. (Without
	// the skip, 100 / self.id on id=0 would fold to nothing and fail the row too.)
	belt := "type NonZero = int where self != 0\nmaster Skill {\n" +
		"  record { id: NonZero }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert 100 / self.id > 0 } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id\n5\n0\n",
	})
	d := single(t, diags, master.CodeCellRefinement)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want only the refinement on row 3", d.Message)
	}
}

func TestLoadRowValidationCallsRowMethod(t *testing.T) {
	// A per-row check may call a row method: self.balanced() folds on the row
	// record, so the master backs its row's method table here. The second row is
	// unbalanced (power < cost) and is the only one reported.
	belt := "master Skill {\n" +
		"  record { id: int, cost: int, power: int } impl {\n" +
		"    pub balanced(): bool { return self.power >= self.cost }\n" +
		"  }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert self.balanced() } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost,power\n1,10,30\n2,50,20\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationMethodAssertFires(t *testing.T) {
	// A row method called by a check carries its own assert. A row that violates
	// it (power < 0) fails validation: the assert fires during the fold — the
	// method folds to an error the check faults — rather than being skipped.
	belt := "master Skill {\n" +
		"  record { id: int, power: int } impl {\n" +
		"    pub ok(): bool {\n      assert self.power >= 0\n      return true\n    }\n" +
		"  }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert self.ok() } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,power\n1,5\n2,-1\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationMethodAssertThroughExpr(t *testing.T) {
	// A helper's failed assert is faulted even when the call is wrapped in another
	// expression (== true): the violation travels on its own channel, not as a
	// value the surrounding fold would swallow to nil.
	belt := "master Skill {\n" +
		"  record { id: int, power: int } impl {\n" +
		"    pub ok(): bool {\n      assert self.power >= 0\n      return true\n    }\n" +
		"  }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert self.ok() == true } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,power\n1,5\n2,-1\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationLambdaClosesOverSelf(t *testing.T) {
	// A check may wrap its row predicate in a function literal that closes over
	// self; the lambda folds against the row, so a valid row passes and only the
	// failing one (id <= 0) is reported — proving self reaches the lambda body.
	belt := "master Skill {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert (fn(): bool { return self.id > 0 })() } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id\n1\n-1\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationAliasedRowMethod(t *testing.T) {
	// The row is reached through an alias chain (record Row, type Row = Base); a
	// row method on it still folds, so a row failing the method (power < cost) is
	// reported.
	belt := "type Base = { id: int, cost: int, power: int }\ntype Row = Base\n" +
		"master Skill {\n" +
		"  record Row impl {\n" +
		"    pub ok(): bool { return self.power >= self.cost }\n" +
		"  }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert self.ok() } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost,power\n1,10,30\n2,50,20\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the failing row data/skills.csv:3", d.Message)
	}
}

func TestLoadRowValidationUnevaluableRowFails(t *testing.T) {
	// A check folds to a definite true for most rows but not for one whose divisor
	// is zero, where it cannot be evaluated. A check that cannot confirm a row is
	// valid fails it (fail-safe), so that row — and only that row — is reported.
	belt := "master Skill {\n" +
		"  record { id: int, cost: int }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"  validate { each { assert 100 / self.cost >= 0 } }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,cost\n1,5\n2,0\n",
	})
	d := single(t, diags, master.CodeRowValidationFailed)
	if !strings.Contains(d.Message, "data/skills.csv:3") {
		t.Errorf("message = %q, want the unevaluable row data/skills.csv:3", d.Message)
	}
}

func TestLoadDuplicatePrimaryKey(t *testing.T) {
	// The third data row repeats id 1, so it is the duplicate; the diagnostic
	// points at its key cell and names the first occurrence's row.
	belt := "master Skill {\n  record { id: int, name: string }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name\n1,Heal\n2,Fire\n1,Frost\n",
	})
	d := single(t, diags, master.CodeDuplicatePrimaryKey)
	for _, frag := range []string{"data/skills.csv:4,1", "id=1", "first at row 2"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

func TestLoadDuplicateCompositePrimaryKey(t *testing.T) {
	// The third row repeats the (skill, level) = (1, 1) tuple; the diagnostic
	// renders the whole key and anchors at the row's first key column.
	belt := "master Upgrade {\n  record { skill: int, level: int, cost: int }\n  primary (skill, level)\n  source { csv \"u.csv\" }\n}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/u.csv": "skill,level,cost\n1,1,10\n1,2,20\n1,1,30\n",
	})
	d := single(t, diags, master.CodeDuplicatePrimaryKey)
	for _, frag := range []string{"data/u.csv:4,1", "skill=1, level=1", "first at row 2"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

func TestLoadUniquePrimaryKeyClean(t *testing.T) {
	// Distinct keys report nothing — the same value in a non-key column does not
	// collide.
	belt := "master Skill {\n  record { id: int, name: string }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name\n1,Heal\n2,Heal\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
}

func TestLoadMissingColumn(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name\n1,Heal\n",
	})
	d := single(t, diags, master.CodeMissingColumn)
	if !strings.Contains(d.Message, "power") {
		t.Errorf("message = %q, want it to name the missing field", d.Message)
	}
}

func TestLoadBasePathResolves(t *testing.T) {
	// The locator resolves under the base path the caller gives, nowhere else.
	belt := "master Skill {\n  record { id: int, name: string }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "assets/master"}, map[string]string{
		"assets/master/skills.csv": "id,name\n1,Heal\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if loaded[0].Display != "assets/master/skills.csv" {
		t.Errorf("display = %q, want the base-path-resolved name", loaded[0].Display)
	}
}

func TestLoadLocatorEscapesRoot(t *testing.T) {
	// A locator climbing past the root with `..` is refused, not read.
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"../../secret.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, nil)
	d := single(t, diags, master.CodeLocatorEscapesRoot)
	if !strings.Contains(d.Message, "secret.csv") {
		t.Errorf("message = %q, want it to name the locator", d.Message)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing read for an escaping locator", loaded)
	}
}

func TestLoadAbsoluteLocatorRejected(t *testing.T) {
	// An absolute locator is refused rather than silently joined under the root
	// and read with a misleading absolute-looking name.
	belt := "master M {\n  record { id: int }\n  primary id\n  source { csv \"/etc/data.csv\" }\n}\n"
	loaded, diags := run(t, belt, nil, nil)
	single(t, diags, master.CodeLocatorEscapesRoot)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for an absolute locator", loaded)
	}
}

func TestLoadLocatorEscapesBase(t *testing.T) {
	// A locator that climbs out of the base path with `..` escapes its source
	// directory even if it stays under the project root; it is refused.
	belt := "master M {\n  record { id: int }\n  primary id\n  source { csv \"../m.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, nil)
	single(t, diags, master.CodeLocatorEscapesRoot)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for a base-escaping locator", loaded)
	}
}

func TestLoadDriveQualifiedLocatorRejected(t *testing.T) {
	// A Windows drive-qualified locator is absolute on Windows; off Windows
	// filepath.IsAbs misses it, so it is refused by the cross-platform check
	// rather than read as $root/C:/data.csv.
	belt := "master M {\n  record { id: int }\n  primary id\n  source { csv \"C:/data.csv\" }\n}\n"
	loaded, diags := run(t, belt, nil, nil)
	single(t, diags, master.CodeLocatorEscapesRoot)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for a drive-qualified locator", loaded)
	}
}

func TestLoadSkipsReadOnOptionError(t *testing.T) {
	// An invalid option must not be read with a fallback default; the source is
	// reported and left unread.
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"skills.csv\" { delimiter: 5 } }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/skills.csv": "id\n1\n"})
	single(t, diags, master.CodeOptionTypeMismatch)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing read when an option is invalid", loaded)
	}
}

func TestLoadSkipsCoercionOnReadError(t *testing.T) {
	// The csv is absent: the read fails, and the failure is reported on its own
	// — not buried under a missing-column error for every field, nor an empty
	// table printed as if it had loaded.
	loaded, diags := run(t, skillBelt, map[string]string{"csv": "data"}, nil)
	single(t, diags, master.CodeSourceUnreadable)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing on a failed read", loaded)
	}
}

func TestLoadFollowsAliasChain(t *testing.T) {
	// Level is a plain alias of the refined Positive; the predicate must still
	// run through the alias.
	belt := "type Positive = int where self > 0\n" +
		"type Level = Positive\n\n" +
		"master Skill {\n" +
		"  record { id: int, power: Level }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,power\n1,-5\n",
	})
	d := single(t, diags, master.CodeCellRefinement)
	if !strings.Contains(d.Message, "-5") {
		t.Errorf("message = %q, want the violating value", d.Message)
	}
}

func TestLoadGenericRowUnsupported(t *testing.T) {
	// A generic row alias is a shape the reader does not expand; rather than
	// silently skipping the master's sources, it is reported as unsupported.
	belt := "type Row<T> = { id: T }\n\n" +
		"master M {\n  record Row<int>\n  primary id\n  source { csv \"m.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/m.csv": "id\n1\n"})
	single(t, diags, master.CodeUnsupportedRowType)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for an unsupported row", loaded)
	}
}

func TestLoadGenericFieldUnsupported(t *testing.T) {
	// A generic-alias field type is reported as unsupported rather than silently
	// dropped; the concrete fields around it still bind.
	belt := "type Id<T> = T\n\n" +
		"master Skill {\n  record { id: int, val: Id<int> }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/skills.csv": "id,val\n1,2\n"})
	d := single(t, diags, master.CodeUnsupportedFieldType)
	if !strings.Contains(d.Message, "val") {
		t.Errorf("message = %q, want it to name the generic field", d.Message)
	}
}

func TestLoadDuplicateRowFieldRejected(t *testing.T) {
	// A row declaring the same field twice is ambiguous (which type binds the
	// cell, which refinement runs); it is reported rather than loaded.
	belt := "type Positive = int where self > 0\n\n" +
		"master Skill {\n  record { id: int, x: Positive, x: int }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/skills.csv": "id,x\n1,-5\n"})
	d := single(t, diags, master.CodeDuplicateRowField)
	if !strings.Contains(d.Message, "x") {
		t.Errorf("message = %q, want it to name the duplicated field", d.Message)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for a duplicate-field row", loaded)
	}
}

func TestLoadDuplicateOptionRejected(t *testing.T) {
	// A repeated option would resolve to the last value silently; it is reported
	// and the source left unread.
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"skills.csv\" { delimiter: \";\", delimiter: \",\" } }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/skills.csv": "id\n1\n"})
	single(t, diags, master.CodeDuplicateOption)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing read for a duplicate option", loaded)
	}
}

func TestLoadUnknownFormat(t *testing.T) {
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { xlsx \"skills.xlsx\" }\n}\n"
	_, diags := run(t, belt, nil, nil)
	d := single(t, diags, master.CodeUnknownFormat)
	if !strings.Contains(d.Message, "xlsx") {
		t.Errorf("message = %q, want it to name the unknown format", d.Message)
	}
}

func TestLoadBadOptions(t *testing.T) {
	for _, c := range []struct {
		name, opts string
		code       diagnostic.Code
	}{
		{"unknown key", "{ sheet: \"S1\" }", master.CodeUnknownOption},
		{"wrong type", "{ delimiter: 5 }", master.CodeOptionTypeMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"skills.csv\" " + c.opts + " }\n}\n"
			_, diags := run(t, belt, nil, map[string]string{"skills.csv": "id\n1\n"})
			single(t, diags, c.code)
		})
	}
}

// single asserts diags holds exactly one diagnostic of the given code.
func single(t *testing.T, diags []diagnostic.Diagnostic, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one %s", diags, code)
	}
	if diags[0].Code != code {
		t.Fatalf("code = %s, want %s", diags[0].Code, code)
	}
	return diags[0]
}
