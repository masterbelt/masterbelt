package load_test

import (
	"os"
	"path/filepath"
	"strconv"
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
// fits(self.sum(...)) must not pass on a 300 smuggled past the annotation. The
// overflowing argument leaves the call unfoldable and the check fails safe. The
// aggregate sits in a static fn (the slice's reachable shape — a bare relation query
// in the check condition is rejected by the analyzer until a later slice drives it).
func TestValidateAllAggregateIntoNarrowParam(t *testing.T) {
	const belt = "fn fits(x: sbyte): bool {\n  return true\n}\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n      return fits(self.sum(fn(c) -> c.cost))\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("fits(self.sum) with sum 300 into sbyte: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
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
		return "master Cards {\n  record { id: int, cost: int, big: nint } impl {\n" +
			"    pub static fn s(): nint {\n      return self.sum(fn(c) -> c." + sel + ")\n    }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.s() " + cmp + "\n    }\n  }\n" +
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

// TestValidateAllAggregateIntoRefinedParam pins that a relation aggregate flowing
// into a non-union refined type is checked against its predicate, not only its
// width: a sum of 0 does not satisfy Positive (self > 0), so takes(Cards.sum(...))
// must not pass on a 0 admitted only by the integer range. The violating argument
// leaves the call unfoldable and the check fails safe.
func TestValidateAllAggregateIntoRefinedParam(t *testing.T) {
	const belt = "type Positive = int where self > 0\n" +
		"fn takes(x: Positive): bool {\n  return true\n}\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n      return takes(self.sum(fn(c) -> c.cost))\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,0\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("takes(self.sum) with sum 0 into Positive: table_validation_failed = %d, want 1 (0 violates self > 0)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIntoReassignment pins that a data-dependent aggregate
// reassigned into a sized local is range-checked against the local's type, not bound
// raw: a sum of 300 reassigned into an sbyte local must not pass. The narrowing's
// adaption refuses the out-of-range value, leaving the body unfoldable and the check
// failing safe.
func TestValidateAllAggregateIntoReassignment(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n" +
		"      let x: sbyte = 1\n      x = self.sum(fn(c) -> c.cost)\n      return x >= 0\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("reassign sum 300 into sbyte: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIntoCollectionElement pins that an aggregate nested in a
// collection literal is checked against the element type: a sum of 300 in a
// list<sbyte> must not pass. The element's adaption refuses the out-of-range value.
func TestValidateAllAggregateIntoCollectionElement(t *testing.T) {
	const belt = "fn takes(xs: list<sbyte>): bool {\n  return true\n}\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n      return takes([self.sum(fn(c) -> c.cost)])\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,100\n2,100\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("sum 300 as a list<sbyte> element: table_validation_failed = %d, want 1 (300 does not fit sbyte)", countTableFailures(diags))
	}
}

// TestValidateAllAggregateIntoRefinedConversion pins that an explicit conversion of an
// aggregate to a refined type is checked against its predicate: Positive(sum) over a
// sum of 0 must not pass, since 0 violates self > 0. The conversion's adaption refuses
// the predicate-violating value.
func TestValidateAllAggregateIntoRefinedConversion(t *testing.T) {
	const belt = "type Positive = int where self > 0\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn ok(): bool {\n      return Positive(self.sum(fn(c) -> c.cost)) >= 0\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.ok()\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,0\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("Positive(self.sum 0): table_validation_failed = %d, want 1 (0 violates self > 0)", countTableFailures(diags))
	}
}

// TestValidateAllRelationShadowedByLoopVar pins that a for-loop variable reusing the
// name of a relation local reads the iteration value, not the outer relation: m starts
// as a relation over three rows, the loop rebinds m to a two-element list, and
// m.count() in the body must be the list count (2), not the relation's row count (3).
func TestValidateAllRelationShadowedByLoopVar(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn f(): nint {\n" +
		"      let m = self.where(fn(c) -> c.id > 0)\n" +
		"      for m of [[1, 2]] {\n        return m.count()\n      }\n      return 0\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n5\n15\n25\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("loop var shadowing relation m, Cards.f() == 2: table_validation_failed = %d, want 0 (the loop value counts 2, not the relation's 3)", countTableFailures(diags))
	}
}

// TestValidateAllStaticParamFiltersRelation pins that a static fn's parameter
// substitutes into a where predicate it is captured in: average(min) filters the
// rows to cost > min before averaging, so the parameter's value reaches the driver
// as a bound literal. Over costs 10/30/100, average(20) is 65 (the rows above 20)
// and average(0) is 46 (all rows) — different answers prove the parameter filters
// rather than being ignored.
func TestValidateAllStaticParamFiltersRelation(t *testing.T) {
	mk := func(arg, cmp string) string {
		return "master Cards {\n  record { id: int, cost: int } impl {\n" +
			"    pub static fn average(min: int): int {\n" +
			"      let m = self.where(fn(c) -> c.cost > min)\n" +
			"      return m.sum(fn(c) -> c.cost) / m.count()\n" +
			"    }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.average(" + arg + ") " + cmp + "\n    }\n  }\n" +
			"  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, mk("20", "== 65"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("average(20) == 65: table_validation_failed = %d, want 0 (rows above 20 are 30,100 → 65)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("0", "== 46"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("average(0) == 46: table_validation_failed = %d, want 0 (all rows → 140/3 = 46)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("20", "== 46"), bases, data); countTableFailures(diags) != 1 {
		t.Errorf("average(20) == 46: table_validation_failed = %d, want 1 (the parameter must filter, so it is 65 not 46)", countTableFailures(diags))
	}
}

// TestValidateAllCapturedLetFiltersRelation pins that a let bound in the same body
// substitutes into a where predicate that captures it: let min = 20; ... cost > min
// reaches the driver as a bound literal, so the filtered count is the rows above 20.
func TestValidateAllCapturedLetFiltersRelation(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn above(): nint {\n      let min = 20\n      return self.where(fn(c) -> c.cost > min).count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.above() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("captured let cost > min(20): table_validation_failed = %d, want 0 (two rows above 20)", countTableFailures(diags))
	}
}

// TestValidateAllRelationLetCapturesScalarAtBinding pins that a relation let captures
// its predicate's scalars at the binding, not at the later aggregate: min is 20 when m
// is bound, then reassigned to 90 before m.count(), so the count is the rows above 20
// (the value m was built with) and not above 90 — a closure captures its locals at
// creation, and a saved relation must do the same.
func TestValidateAllRelationLetCapturesScalarAtBinding(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn f(): nint {\n" +
		"      let min = 20\n      let m = self.where(fn(c) -> c.cost > min)\n      min = 90\n      return m.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("relation captured at binding (min 20, then 90): table_validation_failed = %d, want 0 (count of cost > 20 is 2, not > 90)", countTableFailures(diags))
	}
}

// TestValidateAllCorrelatedNestedAggregateDeclined pins that a nested relation
// aggregate a predicate reads the outer query binding through is not folded to a
// constant during scalar substitution: the inner count over c.cost is row-dependent,
// so it must ride along for the driver to decline, and the check fails safe. Were the
// whole subexpression folded, the inner correlated comparison would lower as a
// same-table one (always false), collapsing the outer filter to c.id > 0 — counting
// every row — so this asserts the count that wrong fold would produce and requires it
// to be rejected instead.
func TestValidateAllCorrelatedNestedAggregateDeclined(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn f(): nint {\n" +
		"      return self.where(fn(c) -> c.id > int(self.where(fn(d) -> d.cost < c.cost).count())).count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 1 {
		t.Errorf("correlated nested aggregate Cards.f() == 3: table_validation_failed = %d, want 1 (the row-dependent inner count must not fold to a constant)", countTableFailures(diags))
	}
}

// TestValidateAllTernaryCaptureSubstitutes pins that a capture inside a ternary is
// reached: above(useHigh) filters c.cost > (useHigh ? 50 : 20), so the captured bool
// is substituted in the ternary's condition and the data-independent ternary folds to
// the chosen threshold, rather than the reference surviving and being declined.
func TestValidateAllTernaryCaptureSubstitutes(t *testing.T) {
	mk := func(arg, cmp string) string {
		return "master Cards {\n  record { id: int, cost: int } impl {\n" +
			"    pub static fn above(useHigh: bool): nint { return self.where(fn(c) -> c.cost > (useHigh ? 50 : 20)).count() }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.above(" + arg + ") " + cmp + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, mk("true", "== 1"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("above(true) == 1: table_validation_failed = %d, want 0 (one row above 50)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("false", "== 2"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("above(false) == 2: table_validation_failed = %d, want 0 (two rows above 20)", countTableFailures(diags))
	}
}

// TestValidateAllHelperWrappedCaptureSubstitutes pins that a capture inside a pure
// helper call is reached: above(min) filters c.cost > inc(min), so the captured min
// is substituted inside the FuncCall and the data-independent inc(min) folds to the
// threshold, rather than the reference surviving and the query being declined. The
// parameter flows: above(9) keeps the rows above inc(9)=10, above(29) those above 30.
func TestValidateAllHelperWrappedCaptureSubstitutes(t *testing.T) {
	mk := func(arg, cmp string) string {
		return "fn inc(n: int): int { return n + 1 }\n" +
			"master Cards {\n  record { id: int, cost: int } impl {\n" +
			"    pub static fn above(min: int): nint { return self.where(fn(c) -> c.cost > inc(min)).count() }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.above(" + arg + ") " + cmp + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, mk("9", "== 2"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("above(9) with inc(min): table_validation_failed = %d, want 0 (rows above inc(9)=10 are 30,100)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("29", "== 1"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("above(29) with inc(min): table_validation_failed = %d, want 0 (one row above inc(29)=30)", countTableFailures(diags))
	}
}

// TestValidateAllRefinedCaptureConversionChecked pins that folding a captured operand
// still vets a refined conversion against its predicate: above(min) compares c.cost
// against Positive(min), so above(5) drives (5 inhabits Positive) but above(-1) must
// not — -1 violates self > 0, so the conversion is refused and the check fails safe
// rather than folding to -1 and counting rows. The fold keeps the data-aware checks
// even though it declines relation aggregates.
func TestValidateAllRefinedCaptureConversionChecked(t *testing.T) {
	mk := func(arg, cmp string) string {
		return "type Positive = int where self > 0\n" +
			"master Cards {\n  record { id: int, cost: int } impl {\n" +
			"    pub static fn above(min: int): nint { return self.where(fn(c) -> c.cost > Positive(min)).count() }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.above(" + arg + ") " + cmp + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, mk("5", "== 3"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("above(5) with Positive(5): table_validation_failed = %d, want 0 (all three rows exceed 5)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("-1", "== 3"), bases, data); countTableFailures(diags) != 1 {
		t.Errorf("above(-1) with Positive(-1): table_validation_failed = %d, want 1 (-1 violates self > 0, so the conversion is refused)", countTableFailures(diags))
	}
}

// TestValidateAllNominalMethodCaptureSubstitutes pins that a capture used as the
// receiver of a method on a nominal scalar is folded with its type intact: min is a
// Threshold and the predicate compares c.cost against min.bump(), so the method folds
// to a constant (the fold keeps the receiver's type, resolving the method) and the
// threshold drives the filter, rather than the typed reference being replaced by a
// bare literal that no longer resolves the method.
func TestValidateAllNominalMethodCaptureSubstitutes(t *testing.T) {
	const belt = "type Threshold = int impl { pub bump(): Threshold { return self } }\n" +
		"master Cards {\n  record { id: int, cost: Threshold } impl {\n" +
		"    pub static fn above(min: Threshold): nint { return self.where(fn(c) -> c.cost > min.bump()).count() }\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.above(20) == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("above(20) with min.bump(): table_validation_failed = %d, want 0 (two rows above 20)", countTableFailures(diags))
	}
}

// TestValidateAllConversionWrappedCaptureSubstitutes pins that a capture wrapped in an
// explicit conversion is reached: above(min) filters c.cost > int(min), so the scalar
// inside the conversion is substituted and the data-independent conversion folds,
// rather than the reference surviving into the chain and being declined.
func TestValidateAllConversionWrappedCaptureSubstitutes(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn above(min: int): nint { return self.where(fn(c) -> c.cost > int(min)).count() }\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.above(20) == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("above(20) with int(min): table_validation_failed = %d, want 0 (two rows above 20)", countTableFailures(diags))
	}
}

// TestValidateAllStringParamFiltersRelation pins that the substitution carries a
// string parameter too: byName(target) filters a string column to the rows equal to
// target, so the count is those rows.
func TestValidateAllStringParamFiltersRelation(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, name: string } impl {\n" +
		"    pub static fn named(target: string): nint {\n      return self.where(fn(c) -> c.name == target).count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.named(\"a\") == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,name\n1,a\n2,b\n3,a\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("named(\"a\") == 2: table_validation_failed = %d, want 0 (two rows named a)", countTableFailures(diags))
	}
}

// TestValidateAllRelationValuedParam pins that a relation flows as a value into a
// helper: cnt takes a relation<Cards> and counts it, and f calls it with a narrowed
// relation, so the count runs against the rows the argument selects.
func TestValidateAllRelationValuedParam(t *testing.T) {
	const belt = "fn cnt(r: relation<Cards>): nint {\n  return r.count()\n}\n" +
		"master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn f(): nint { return cnt(self.where(fn(c) -> c.cost > 20)) }\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("cnt(self.where(cost > 20)) == 2: table_validation_failed = %d, want 0 (the relation argument counts two rows)", countTableFailures(diags))
	}
}

// TestValidateAllRelationLocalReassignment pins that a relation local can be
// reassigned: m starts as all rows above 0, is narrowed to those above 20, and the
// final count reflects the reassigned relation (2), not the first (3).
func TestValidateAllRelationLocalReassignment(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn f(): nint {\n" +
		"      let m = self.where(fn(c) -> c.cost > 0)\n" +
		"      m = m.where(fn(c) -> c.cost > 20)\n" +
		"      return m.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("reassigned relation m.count() == 2: table_validation_failed = %d, want 0 (the narrowed relation counts two, not the original three)", countTableFailures(diags))
	}
}

// TestValidateAllRelationCapturedByClosure pins that a closure captures a relation
// local: m is narrowed before the closure, and the closure's count runs against it.
func TestValidateAllRelationCapturedByClosure(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int } impl {\n    pub static fn f(): nint {\n" +
		"      let m = self.where(fn(c) -> c.cost > 20)\n" +
		"      return (fn(): nint { return m.count() })()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,10\n2,30\n3,100\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("closure capturing relation m, m.count() == 2: table_validation_failed = %d, want 0 (the captured relation counts two)", countTableFailures(diags))
	}
}

// TestValidateAllRelationAliasMethod pins that a user method on a nominal relation
// alias dispatches on the relation value: CardRel aliases relation<Cards> with a cnt
// method, and a relation bound at that type runs cnt against the rows. A built-in
// relation method the alias does not own falls through to ordinary dispatch.
func TestValidateAllRelationAliasMethod(t *testing.T) {
	const belt = "type CardRel = relation<Cards> impl { pub cnt(): nint { return self.count() } }\n" +
		"master Cards {\n  record { id: int } impl {\n    pub static fn f(): nint {\n" +
		"      let r: CardRel = self.where(fn(c) -> c.id > 1)\n      return r.cnt()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n3\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("relation alias method r.cnt() == 2: table_validation_failed = %d, want 0 (the alias method counts two rows)", countTableFailures(diags))
	}
}

// TestValidateAllRelationAliasOverridesBuiltin pins that a relation alias method whose
// name shadows a built-in relation method (count, sum, where) wins over the built-in,
// the way the checker resolves the call — so the override's body runs, not the row
// count. Here count() is overridden to return 42 over three rows, and the all clause
// asserts 42: the built-in row count would be 3, so a check that intercepted the
// built-in name before the override would fail.
func TestValidateAllRelationAliasOverridesBuiltin(t *testing.T) {
	const belt = "type CardRel = relation<Cards> impl { pub count(): nint { return 42 } }\n" +
		"master Cards {\n  record { id: int } impl {\n    pub static fn f(): nint {\n" +
		"      let r: CardRel = self.where(fn(c) -> c.id > 0)\n      return r.count()\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 42\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n3\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("relation alias count() override returns 42: table_validation_failed = %d, want 0 (the override wins over the built-in row count 3)", countTableFailures(diags))
	}
}

// TestValidateAllRelationUnionMatchLet pins that a relation held in a union-typed local
// is selected by a match: x is relation<Cards> | error, bound to a filtered relation, and
// the match's relation<Cards> arm counts its rows. The scrutinee is tagged with its
// relation member from the binding's structural type, so the arm dispatch must compare
// the relation tag against the relation arm — two rows match id > 1.
func TestValidateAllRelationUnionMatchLet(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n    pub static fn f(): nint {\n" +
		"      let x: relation<Cards> | error = self.where(fn(c) -> c.id > 1)\n" +
		"      match x { relation<Cards> r -> { return r.count() } error e -> { return 0 } }\n    }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n3\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("match over a union-typed relation local: table_validation_failed = %d, want 0 (the relation arm counts two rows)", countTableFailures(diags))
	}
}

// TestValidateAllRelationUnionMatchStaticParam pins the same dispatch when the relation
// flows into a static fn's union-typed parameter: g(x: relation<Cards> | error) matches x,
// and the caller passes a filtered relation. The argument is tagged with its relation
// member at the call, so the match in g selects the relation arm — two rows.
func TestValidateAllRelationUnionMatchStaticParam(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n" +
		"    pub static fn g(x: relation<Cards> | error): nint { match x { relation<Cards> r -> { return r.count() } error e -> { return 0 } } }\n" +
		"    pub static fn f(): nint { return Cards.g(self.where(fn(c) -> c.id > 1)) }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n3\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("match over a union-typed relation parameter: table_validation_failed = %d, want 0 (the relation arm counts two rows)", countTableFailures(diags))
	}
}

// TestValidateAllRelationUnionMatchReturn pins the same dispatch when the relation is the
// relation member of a union return type: h(): relation<Cards> | error returns a filtered
// relation, and the caller matches h(). The return value is tagged structurally, so the
// match selects the relation arm — two rows.
func TestValidateAllRelationUnionMatchReturn(t *testing.T) {
	const belt = "master Cards {\n  record { id: int } impl {\n" +
		"    pub static fn h(): relation<Cards> | error { return self.where(fn(c) -> c.id > 1) }\n" +
		"    pub static fn f(): nint { match Cards.h() { relation<Cards> r -> { return r.count() } error e -> { return 0 } } }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.f() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id\n1\n2\n3\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("match over a union return holding a relation: table_validation_failed = %d, want 0 (the relation arm counts two rows)", countTableFailures(diags))
	}
}

// TestValidateAllDirectFilteredCount pins that a relation query written directly in a
// validate all check — not reached through a static fn — drives against the loaded rows:
// Cards.where(power > 10).count() is two of three rows, asserted == 2. The bare row count
// is three, so this passes only because the filter actually runs.
func TestValidateAllDirectFilteredCount(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(fn(c) -> c.power > 10).count() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("direct filtered count == 2: table_validation_failed = %d, want 0 (two rows have power > 10)", countTableFailures(diags))
	}
}

// TestValidateAllDirectFilteredCountRejectsWrong is the negative twin: the same direct
// query asserted == 3 (the row count, not the filtered count) must fail, proving the
// filter runs rather than the check reading the bare row count.
func TestValidateAllDirectFilteredCountRejectsWrong(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(fn(c) -> c.power > 10).count() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("direct filtered count == 3 must fail: only two rows have power > 10, so a pass means the filter was not run")
	}
}

// TestValidateAllDirectFilteredSum pins a direct filtered sum: the powers above 10 are
// 20 and 30, summing to 50.
func TestValidateAllDirectFilteredSum(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(fn(c) -> c.power > 10).sum(fn(c) -> c.power) == 50\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("direct filtered sum == 50: table_validation_failed = %d, want 0 (20 + 30)", countTableFailures(diags))
	}
}

// TestValidateAllDirectExplicitCount pins the explicit count method on the master
// relation (Cards.count(), distinct from the bare count keyword) folding to the row
// count.
func TestValidateAllDirectExplicitCount(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.count() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("direct explicit count == 3: table_validation_failed = %d, want 0", countTableFailures(diags))
	}
}

// TestValidateAllToListMaterializesRows pins that to_list() materializes the
// relation's rows as a list the check reads: the unfiltered list has every row, and
// a filtered list reads the actual field values of the rows the predicate keeps —
// power > 10 keeps two rows, the first of which has power 20.
func TestValidateAllToListMaterializesRows(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	for _, c := range []struct {
		name string
		body string
	}{
		{"all rows", "Cards.to_list().len() == 3"},
		{"filtered count", "Cards.where(fn(c) -> c.power > 10).to_list().len() == 2"},
		{"filtered field", "Cards.where(fn(c) -> c.power > 10).to_list()[0].power == 20"},
	} {
		belt := "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
			"  validate {\n    all {\n      assert " + c.body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s): table_validation_failed = %d, want 0", c.name, c.body, countTableFailures(diags))
		}
	}
}

// TestValidateAllToListRejectsWrong is the negative twin: a to_list assertion that
// does not hold of the real rows must fail, proving the list carries the actual data
// rather than passing vacuously.
func TestValidateAllToListRejectsWrong(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	for _, body := range []string{
		"Cards.where(fn(c) -> c.power > 10).to_list().len() == 3",
		"Cards.to_list()[0].power == 99",
	} {
		belt := "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
			"  validate {\n    all {\n      assert " + body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
			t.Errorf("%q must fail: the assertion does not hold of the real rows", body)
		}
	}
}

// TestValidateAllLimitCapsRows pins that limit(n) caps the materialized rows to n, in
// the relation's (insert) order: limit(2).to_list() is the first two rows, so its
// first element is row id 1; a filtered limit reads the first kept row's field.
func TestValidateAllLimitCapsRows(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	for _, c := range []struct {
		name string
		body string
	}{
		{"cap count", "Cards.limit(2).to_list().len() == 2"},
		{"cap order", "Cards.limit(2).to_list()[0].id == 1"},
		{"filter then cap", "Cards.where(fn(c) -> c.power > 10).limit(1).to_list()[0].power == 20"},
	} {
		belt := "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
			"  validate {\n    all {\n      assert " + c.body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s): table_validation_failed = %d, want 0", c.name, c.body, countTableFailures(diags))
		}
	}
}

// TestValidateAllLimitedCountFailsSafe pins the boundary that a count or sum does not
// respect a limit: limit(2).count() over three rows really is two, but because a count
// query ignores the cap the chain is left unfoldable and the check fails safe rather
// than reporting a possibly-wrong number. A limited materialization (to_list) is the
// supported way to observe a cap.
func TestValidateAllLimitedCountFailsSafe(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(2).count() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("limit(2).count() == 2 must fail safe: count ignores the limit, so the chain is left unfoldable")
	}
}

// TestValidateAllLimitRefinedArgFailsSafe pins that a limit whose cap is a refined
// conversion the data violates fails safe rather than rendering the unchecked literal:
// Positive(n) for a data-dependent n of 0 (the count of a relation no row matches) is a
// refused conversion, so the cap is left whole and the materialization is left
// unfoldable. Without the admission guard the cap would fold to LIMIT 0 and the empty
// list would pass the assertion through a conversion that should have been refused.
func TestValidateAllLimitRefinedArgFailsSafe(t *testing.T) {
	const belt = "type Positive = nint where self > 0\n" +
		"master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(Positive(Cards.where(fn(c) -> c.power > 1000).count())).to_list().len() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("limit(Positive(0)) must fail safe: 0 does not inhabit Positive, so the refused conversion must not render LIMIT 0")
	}
}

// TestValidateAllLimitClampsHugeCap pins that a cap wider than int64 — a valid nint the
// signature accepts — clamps to no effective cap rather than failing safe: limit of a
// value past int64 reads every row, since no table holds that many.
func TestValidateAllLimitClampsHugeCap(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(9223372036854775808).to_list().len() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("limit(2^63) must read every row: table_validation_failed = %d, want 0 (a cap past int64 is no effective cap)", countTableFailures(diags))
	}
}

// TestValidateAllLimitBeforeWhereFailsSafe pins that a where applied after a limit fails
// safe rather than filtering outside the capped relation: limit(1) keeps row 1 (id 1),
// which where(id > 1) drops, so the result is empty. The flat WHERE-then-LIMIT render
// cannot express filter-after-cap, so the chain is left unfoldable — and must not return
// row 2, which it would by filtering the whole table then capping.
func TestValidateAllLimitBeforeWhereFailsSafe(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(1).where(fn(c) -> c.id > 1).to_list()[0].id == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,20\n3,30\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("limit(1).where(id > 1) must fail safe: limit keeps row 1, which the filter drops, so it must not return row 2")
	}
}

// TestValidateAllOrderSortsRows pins that order sorts the materialized rows by the
// column the selector names, in the chosen direction: with powers 5, 30, 20, the
// descending order reads 30 first (id 2) and the ascending order reads 5 first, and a
// filter, order, and limit compose — the smallest power above 10 is 20.
func TestValidateAllOrderSortsRows(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,30\n3,20\n"}
	for _, c := range []struct {
		name string
		body string
	}{
		{"desc top", "Cards.order(fn(c) -> c.power.desc()).to_list()[0].id == 2"},
		{"desc value", "Cards.order(fn(c) -> c.power.desc()).limit(1).to_list()[0].power == 30"},
		{"asc top", "Cards.order(fn(c) -> c.power.asc()).to_list()[0].power == 5"},
		{"filter order cap", "Cards.where(fn(c) -> c.power > 10).order(fn(c) -> c.power.asc()).limit(1).to_list()[0].power == 20"},
		{"order ignored by count", "Cards.order(fn(c) -> c.power.desc()).count() == 3"},
	} {
		belt := "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
			"  validate {\n    all {\n      assert " + c.body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s): table_validation_failed = %d, want 0", c.name, c.body, countTableFailures(diags))
		}
	}
}

// TestValidateAllOrderRejectsWrong is the negative twin: an order assertion that does
// not hold of the sorted rows must fail, proving the rows are really sorted rather than
// passing vacuously — the descending top is power 30, not 5.
func TestValidateAllOrderRejectsWrong(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.order(fn(c) -> c.power.desc()).to_list()[0].power == 5\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,30\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("descending top power == 5 must fail: the highest power is 30, so a pass means the rows were not sorted")
	}
}

// TestValidateAllLimitBeforeOrderFailsSafe pins that an order applied after a limit
// fails safe, the order twin of limit-before-where: the flat render sorts before it
// caps, so capping the unsorted relation then sorting (limit(2).order(...)) cannot be
// expressed and is left unfoldable rather than returning the sorted top of the whole
// table.
func TestValidateAllLimitBeforeOrderFailsSafe(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(2).order(fn(c) -> c.power.desc()).to_list()[0].power == 30\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,30\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("limit(2).order(...) must fail safe: the cap applies before the sort, which the flat render cannot express")
	}
}

// TestValidateAllOrderCustomOrderFailsSafe pins that a column whose type defines its
// own ordering is not sorted by plain SQL: Rank declares lt, so it is orderable (the
// checker accepts the order) but its order is custom, which SQL's ORDER BY on the raw
// integer would not honor. The driver declines the key and the check fails safe rather
// than sorting by the stored value — even here, where the custom order happens to match
// the integer order, the driver must decline because it cannot know that.
func TestValidateAllOrderCustomOrderFailsSafe(t *testing.T) {
	const belt = "type Rank = int impl { pub lt(o: Rank): bool { return self.int() < o.int() } }\n" +
		"master Cards {\n  record { id: int, rank: Rank }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.order(fn(c) -> c.rank.asc()).to_list()[0].id == 1\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,rank\n1,5\n2,30\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("order by a custom-ordered column must fail safe: SQL's native ORDER BY does not carry the type's order")
	}
}

// TestValidateAllOrderIgnoredByCountWithCustomOrder pins that a count over an ordered
// relation folds even when the order is by a custom-ordered column: count discards the
// order, so the unsupportable sort key must not make the count fail — the order is only
// required to be SQL-renderable when the rows are materialized, not when an aggregate
// that ignores it consumes the relation.
func TestValidateAllOrderIgnoredByCountWithCustomOrder(t *testing.T) {
	const belt = "type Rank = int impl { pub lt(o: Rank): bool { return self.int() < o.int() } }\n" +
		"master Cards {\n  record { id: int, rank: Rank }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.order(fn(c) -> c.rank.asc()).count() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,rank\n1,5\n2,30\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("count over a custom-ordered relation: table_validation_failed = %d, want 0 (count ignores the order)", countTableFailures(diags))
	}
}

// TestValidateAllOrderBlockSelectorIgnoredByCount pins that count folds over a relation
// ordered by a selector the driver does not parse — a block-body lambda rather than the
// inline fn(c) -> c.col.asc() shape. Count discards the order, so its selector is not
// parsed at all; only a materialization (to_list) needs the recognized shape. Without
// this the unparsed order would reject the count even though the order is unused.
func TestValidateAllOrderBlockSelectorIgnoredByCount(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.order(fn(c): ordering<Cards> { let x = c.power.desc(); return x }).count() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,5\n2,30\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("count over a relation ordered by a block-body selector: table_validation_failed = %d, want 0 (count ignores the order, so its selector need not parse)", countTableFailures(diags))
	}
}

// TestValidateAllMinMaxExtreme pins that min and max read the least and greatest value
// of a column over the rows: with powers 30, 5, 20 the min is 5 and the max is 30, and a
// filter narrows the rows the extreme is over.
func TestValidateAllMinMaxExtreme(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,30\n2,5\n3,20\n"}
	hdr := "master Cards {\n  record { id: int, power: int } impl {\n"
	for _, c := range []struct {
		name, fn string
		want     int
	}{
		{"min", "self.min(fn(c) -> c.power)", 5},
		{"max", "self.max(fn(c) -> c.power)", 30},
		{"filtered min", "self.where(fn(c) -> c.power > 10).min(fn(c) -> c.power)", 20},
	} {
		belt := hdr + "    pub static fn x(): int { match " + c.fn +
			" { int n -> { return n } null e -> { return -1 } } }\n  }\n" +
			"  primary id\n  validate {\n    all {\n      assert Cards.x() == " + strconv.Itoa(c.want) + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s == %d): table_validation_failed = %d, want 0", c.name, c.fn, c.want, countTableFailures(diags))
		}
	}
}

// TestValidateAllMinMaxEmptyIsNull pins that an extreme over no rows is null, not a
// stray value: a filter no row passes leaves min with nothing, so the null arm runs.
func TestValidateAllMinMaxEmptyIsNull(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int } impl {\n" +
		"    pub static fn x(): int { match self.where(fn(c) -> c.power > 1000).min(fn(c) -> c.power) { int n -> { return n } null e -> { return -1 } } }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.x() == -1\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,30\n2,5\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("min over an empty relation must be null: table_validation_failed = %d, want 0 (the null arm returns -1)", countTableFailures(diags))
	}
}

// TestValidateAllMinMaxRejectsWrong is the negative twin: an extreme assertion that does
// not hold of the real rows must fail, proving the extreme is read rather than assumed —
// the greatest power is 30, not 5.
func TestValidateAllMinMaxRejectsWrong(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int } impl {\n" +
		"    pub static fn x(): int { match self.max(fn(c) -> c.power) { int n -> { return n } null e -> { return -1 } } }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.x() == 5\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,30\n2,5\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("max power == 5 must fail: the greatest power is 30, so a pass means the extreme was not read")
	}
}

// TestValidateAllMinMaxCustomOrderFailsSafe pins that min/max declines a column whose
// type carries a custom order, the extreme twin of order's custom-order rejection: the
// extreme is read by SQL's native order, which would not honor the type's order, so the
// check fails safe.
func TestValidateAllMinMaxCustomOrderFailsSafe(t *testing.T) {
	const belt = "type Rank = int impl { pub lt(o: Rank): bool { return self.int() < o.int() } }\n" +
		"master Cards {\n  record { id: int, rank: Rank } impl {\n" +
		"    pub static fn x(): int { match self.min(fn(c) -> c.rank) { Rank r -> { return r.int() } null e -> { return -1 } } }\n  }\n" +
		"  primary id\n  validate {\n    all {\n      assert Cards.x() == 5\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,rank\n1,30\n2,5\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("min by a custom-ordered column must fail safe: SQL's native order does not carry the type's order")
	}
}

// TestValidateAllOffsetWindow pins that offset skips the first rows in the relation's
// order: offset(1) over three rows leaves two, and order then offset then limit pages —
// ascending [5, 20, 30], skip one, take one, reads 20.
func TestValidateAllOffsetWindow(t *testing.T) {
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,30\n2,5\n3,20\n"}
	for _, c := range []struct {
		name, body string
	}{
		{"skip count", "Cards.offset(1).to_list().len() == 2"},
		{"order page", "Cards.order(fn(c) -> c.power.asc()).offset(1).limit(1).to_list()[0].power == 20"},
		{"window order-independent", "Cards.order(fn(c) -> c.power.asc()).limit(1).offset(1).to_list()[0].power == 20"},
	} {
		belt := "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
			"  validate {\n    all {\n      assert " + c.body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s): table_validation_failed = %d, want 0", c.name, c.body, countTableFailures(diags))
		}
	}
}

// TestValidateAllOffsetBeforeAggregateFailsSafe pins that offset before a count fails
// safe: offset is a window over the materialized rows, which a count discards, so a
// counted offset is left unfoldable rather than counting a skipped window.
func TestValidateAllOffsetBeforeAggregateFailsSafe(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.offset(1).count() == 2\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,power\n1,30\n2,5\n3,20\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("offset(1).count() must fail safe: count discards the window, so an offset count is unfoldable")
	}
}

// TestValidateAllScopeDrives pins that a named scope — a scope-block entry — drives as
// the static fn it desugars to: the body's leading relation method reads against the
// implicit master relation (where(...) is self.where(...)), so a scope composes with
// the algebra (count, sum, a further where) and a parameterized scope filters by its
// argument. expensive() keeps the two rows costing over 100; rarity(legend) the one.
func TestValidateAllScopeDrives(t *testing.T) {
	const decls = "enum Rarity { common; rare; legend }\n" +
		"master Cards {\n  record { id: int, cost: int, rarity: Rarity }\n" +
		"  scope {\n    pub expensive() -> where(fn(c) -> c.cost > 100)\n" +
		"    pub rarity(r: Rarity) -> where(fn(c) -> c.rarity == r)\n" +
		"    pub top(n: nint) -> order(fn(c) -> c.cost.desc()).limit(n)\n  }\n  primary id\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost,rarity\n1,50,common\n2,150,rare\n3,200,legend\n"}
	for _, c := range []struct {
		name, body string
	}{
		{"scope count", "Cards.expensive().count() == 2"},
		{"scope sum", "Cards.expensive().sum(fn(c) -> c.cost) == 350"},
		{"scope composes where", "Cards.expensive().where(fn(c) -> c.cost > 175).count() == 1"},
		{"parameterized scope", "Cards.rarity(Rarity.legend).count() == 1"},
		{"scope chains order and limit", "Cards.top(1).to_list()[0].cost == 200"},
	} {
		belt := decls + "  validate {\n    all {\n      assert " + c.body + "\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
		if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
			t.Errorf("%s (%s): table_validation_failed = %d, want 0", c.name, c.body, countTableFailures(diags))
		}
	}
}

// TestValidateAllScopeRejectsWrong is the negative twin: a check over a scope that does
// not hold of the real rows must fail, proving the scope runs its query rather than
// passing vacuously — expensive() keeps two rows, not three.
func TestValidateAllScopeRejectsWrong(t *testing.T) {
	const belt = "master Cards {\n  record { id: int, cost: int }\n" +
		"  scope {\n    pub expensive() -> where(fn(c) -> c.cost > 100)\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.expensive().count() == 3\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost\n1,50\n2,150\n3,200\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) == 0 {
		t.Error("expensive().count() == 3 must fail: only two rows cost over 100, so a pass means the scope query was not run")
	}
}

// TestValidateAllWideEnumColumnIgnored pins that an enum column whose member value is
// beyond SQLite's range does not disable aggregates over the other columns, the enum
// twin of the wide-integer-column rule: an unused enum with an overwide member is left
// out of the engine, so sum over an int column still drives.
func TestValidateAllWideEnumColumnIgnored(t *testing.T) {
	const belt = "enum Big { huge = 9223372036854775808 }\n" +
		"master Cards {\n  record { id: int, cost: int, b: Big } impl {\n" +
		"    pub static fn s(): nint { return self.where(fn(c) -> c.cost > 0).sum(fn(c) -> c.cost) }\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.s() == 30\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,cost,b\n1,10,huge\n2,20,huge\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("sum(cost) == 30 with a wide enum column: table_validation_failed = %d, want 0 (the overwide enum column must not disable the aggregate)", countTableFailures(diags))
	}
}

// TestValidateAllStringEnumStoresAsText pins that a string-backed enum column stores
// as text, so its values compare lexicographically as the language defines, not as
// numbers: with codes "2" and "10", a filter for codes below "10" matches none ("2"
// orders after "10" as text), whereas numeric storage would wrongly match "2".
func TestValidateAllStringEnumStoresAsText(t *testing.T) {
	const belt = "enum Code: string { lo = \"2\", hi = \"10\" }\n" +
		"master Cards {\n  record { id: int, code: Code } impl {\n" +
		"    pub static fn below(): nint { return self.where(fn(c) -> c.code < Code.hi).count() }\n  }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.below() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	bases := map[string]string{"csv": "data"}
	files := map[string]string{"data/cards.csv": "id,code\n1,lo\n2,hi\n"}
	if _, diags := run(t, belt, bases, files); countTableFailures(diags) != 0 {
		t.Errorf("below() == 0 for a string enum: table_validation_failed = %d, want 0 (\"2\" orders after \"10\" as text, so none is below)", countTableFailures(diags))
	}
}

// TestValidateAllStaticEnumParamFiltersRelation pins the full average_cost canonical:
// a static fn takes an enum parameter and filters its relation by it (c.rarity == r),
// so the enum column loads (read by member name), the parameter substitutes, and the
// engine counts and sums the matching rows. average_cost(common) is 20 (costs 10, 30)
// and average_cost(rare) is 100 (cost 100) — distinct answers prove the enum parameter
// filters rather than being ignored.
func TestValidateAllStaticEnumParamFiltersRelation(t *testing.T) {
	mk := func(arg, cmp string) string {
		return "enum Rarity { common, rare }\n" +
			"master Cards {\n  record { id: int, cost: int, rarity: Rarity } impl {\n" +
			"    pub static fn average_cost(r: Rarity): int {\n" +
			"      let m = self.where(fn(c) -> c.rarity == r)\n" +
			"      return m.sum(fn(c) -> c.cost) / m.count()\n" +
			"    }\n  }\n  primary id\n" +
			"  validate {\n    all {\n      assert Cards.average_cost(" + arg + ") " + cmp + "\n    }\n  }\n" +
			"  source { csv \"cards.csv\" }\n}\n"
	}
	bases := map[string]string{"csv": "data"}
	data := map[string]string{"data/cards.csv": "id,cost,rarity\n1,10,common\n2,30,common\n3,100,rare\n"}
	if _, diags := run(t, mk("Rarity.common", "== 20"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("average_cost(common) == 20: table_validation_failed = %d, want 0 (common costs 10,30 → 20)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("Rarity.rare", "== 100"), bases, data); countTableFailures(diags) != 0 {
		t.Errorf("average_cost(rare) == 100: table_validation_failed = %d, want 0 (rare cost 100)", countTableFailures(diags))
	}
	if _, diags := run(t, mk("Rarity.common", "== 100"), bases, data); countTableFailures(diags) != 1 {
		t.Errorf("average_cost(common) == 100: table_validation_failed = %d, want 1 (the enum parameter must filter, so it is 20 not 100)", countTableFailures(diags))
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
