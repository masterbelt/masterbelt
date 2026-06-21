// These tests pin the bare-column query form: in a relation method's argument a bare
// name reads master M's column, so where(cost > 100) means where(fn(c) -> c.cost >
// 100), the columns binding omitted the way self is. Resolution is last resort — a
// local, parameter, constant, or type of the same name takes that reading instead — and
// a name that is no column of M is still an undefined name. The bare and bound forms
// resolve the same overloads and lower to the same query, so the lambda form must stay
// unambiguous beside the bare overload.
package semantic

import (
	"testing"
)

// TestBareColumnNotUndefined pins that a bare column in a relation method's argument is
// a resolved reference, not an undefined name — the columns binding omitted. It is a
// red→green gate: drop the columns exemption from the reference check (or the
// columnsScope from the checker) and cost is reported undefined.
func TestBareColumnNotUndefined(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(cost > 100).count() == 0\n" +
		"      assert Cards.sum(cost) == 0\n" +
		"      assert Cards.order(cost.desc()).limit(1).count() == 0\n" +
		"    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) {
		t.Errorf("a bare column must not be reported undefined: %v", codes(diags))
	}
}

// TestBareColumnTypoReported pins the precision of the exemption: a name that is no
// column of the relation's master is still an undefined name, so a misspelled column is
// caught. It is the negative twin of TestBareColumnNotUndefined — without it the
// exemption could pass every bare name in a query argument.
func TestBareColumnTypoReported(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(costt > 100).count() == 0\n" +
		"    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a misspelled column must be reported undefined: %v", codes(diags))
	}
}

// TestBareColumnCrossMasterColumn pins that the exemption resolves a column against the
// relation it is read off, not any master in scope: power is a column of Other, not
// Cards, so reading it in a Cards query is undefined. It guards the master-context walk
// against exempting a name that is a column of the wrong master.
func TestBareColumnCrossMasterColumn(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(power > 100).count() == 0\n" +
		"    }\n  }\n  source { csv \"cards.csv\" }\n}\n" +
		"master Other {\n  record { id: int, power: int }\n  primary id\n  source { csv \"other.csv\" }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a column of another master must be undefined in this relation: %v", codes(diags))
	}
}

// TestLambdaWhereNotAmbiguous pins that the explicit lambda form stays unambiguous
// beside the bare overload: a function-literal argument fits only the function-typed
// overload, never the bare predicate one. It is a red→green gate: drop the
// lambda-argument overload filter and where(fn(c) -> ...) matches both overloads and is
// reported ambiguous.
func TestLambdaWhereNotAmbiguous(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(fn(c) -> c.cost > 100).count() == 0\n" +
		"    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeAmbiguousOverload) {
		t.Errorf("the lambda where form must not be ambiguous beside the bare overload: %v", codes(diags))
	}
	if hasCode(diags, CodeUndefinedName) {
		t.Errorf("the lambda where form must resolve cleanly: %v", codes(diags))
	}
}

// TestBareColumnShadowedByParameter pins that the columns reading is last resort: a
// parameter of the same name as a column wins, so a scope fn's r shadows a column r and
// the bare name reads the parameter. The query still type-checks (no undefined, no
// ambiguity), proving the shadow does not break the columns context around it.
func TestBareColumnShadowedByParameter(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n" +
		"  scope {\n    pub costlier(cost: int) -> where(id > cost)\n  }\n" +
		"  primary id\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) {
		t.Errorf("a parameter shadowing a column must resolve, columns still available: %v", codes(diags))
	}
}

// TestBareColumnStaticFnShadow pins that a master that declares a static fn of a query
// method name makes the call a static call, not a relation query: the argument is
// ordinary, so a bare name in it that is not in scope is a genuine undefined name rather
// than an exempted column. It is the gate for the reference check's static-fn guard.
func TestBareColumnStaticFnShadow(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n" +
		"  impl { pub static fn where(b: bool): nint { return 0 } }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(cost > 0) == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a static fn shadowing where makes cost an undefined name: %v", codes(diags))
	}
}

// TestBareColumnEnumMemberResolves pins that a bare enum member compared against an enum
// column (where(rarity == legend)) is resolved through the column's element type, not
// reported as undefined — the reference check mirroring the checker's column-comparison
// enum resolution.
func TestBareColumnEnumMemberResolves(t *testing.T) {
	src := "enum Rarity { common\n  rare\n  legend }\n" +
		"master Cards {\n  record { id: int, rarity: Rarity }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(rarity == legend).count() == 0\n" +
		"      assert Cards.where(rarity == bogusmember).count() == 0\n" +
		"    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	// legend resolves; bogusmember does not — exactly one undefined name, the typo.
	n := 0
	for _, c := range codes(diags) {
		if c == CodeUndefinedName {
			n++
		}
	}
	if n != 1 {
		t.Errorf("legend must resolve and bogusmember must not: undefined_name count = %d, want 1 (%v)", n, codes(diags))
	}
}

// TestBareColumnFunctionValueArg pins that a function-value argument to a query method
// (where(p) for a parameter p of the selector type) matches the lambda overload and
// type-checks cleanly — it is the predicate already, not a bare column to rewrite.
func TestBareColumnFunctionValueArg(t *testing.T) {
	src := "fn counted(p: fn(c: columns<Cards>): predicate<Cards>): nint {\n  return Cards.where(p).count()\n}\n" +
		"master Cards {\n  record { id: int, power: int }\n  primary id\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) || hasCode(diags, CodeNoMatchingOverload) {
		t.Errorf("a function-value where argument must type-check via the lambda overload: %v", codes(diags))
	}
}

// TestBareColumnInvalidChainReported pins that the column exemption follows only a
// relation-returning chain: a query method written after an aggregate
// (Cards.count().where(cost > 0)) is an invalid chain, so its bare name is a genuine
// undefined name rather than an exempted column. It is the gate for the relation-
// returning chain restriction.
func TestBareColumnInvalidChainReported(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.count().where(cost > 0) == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a bare column after an aggregate is an invalid chain, undefined: %v", codes(diags))
	}
}

// TestBareColumnFunctionReceiverReported pins the bare form's receiver scope: it reads
// its columns off a named, qualified, parameter, self, or chained relation — not an
// arbitrary relation-returning function call, whose result type is not resolved where
// the column lowers. A bare column on such a receiver stays an undefined name (the
// lambda form names the binding explicitly there), so the checker, lowering, and
// reference check agree rather than accepting a query that would not fold.
func TestBareColumnFunctionReceiverReported(t *testing.T) {
	src := "fn cards(): relation<Cards> { return Cards }\n" +
		"master Cards {\n  record { id: int, cost: int }\n  primary id\n" +
		"  validate {\n    all {\n      assert cards().where(cost > 0).count() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a bare column on a function-call relation receiver must be reported undefined: %v", codes(diags))
	}
}

// TestBareColumnStaticShadowOnlyDirect pins that a static fn shadows the relation method
// only on a direct master call: a chain (Cards.limit(1).where(cost > 0)) dispatches to the
// relation limit returns, so where is the relation method there and cost is an exempted
// column even though the master declares a static where.
func TestBareColumnStaticShadowOnlyDirect(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n" +
		"  impl { pub static fn where(b: bool): nint { return 0 } }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.limit(1).where(cost > 0).count() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) {
		t.Errorf("a chained where is the relation method, so cost must be an exempted column: %v", codes(diags))
	}
}

// TestBareColumnNestedInnerWins pins that a nested query binds a bare column to the inner
// relation: in Cards.where(id > Other.where(cost > 0).count()) the inner cost is Other's,
// not Cards', so with Cards.cost a bool and Other.cost an int the comparison stands rather
// than being rejected against the outer bool column. It is the gate for the inner-first
// columns stack.
func TestBareColumnNestedInnerWins(t *testing.T) {
	src := "master Other {\n  record { id: int, cost: int }\n  primary id\n  source { csv \"other.csv\" }\n}\n" +
		"master Cards {\n  record { id: int, cost: bool }\n  primary id\n" +
		"  validate {\n    all {\n      assert Cards.where(id > Other.where(cost > 0).count()).count() == 0\n    }\n  }\n  source { csv \"cards.csv\" }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeUndefinedName) || hasCode(diags, CodeInvalidOperation) {
		t.Errorf("the inner cost must bind to Other (int), not the outer Cards (bool): %v", codes(diags))
	}
}

// TestBareColumnQualifiedMaster pins that a query through an imported master
// (deck.Cards.where(cost > 0)) exempts its bare columns the way a local master does, so a
// column read across a namespace is not reported as an undefined name.
func TestBareColumnQualifiedMaster(t *testing.T) {
	diags := analyzeProject(t, map[string]string{
		"cards.belt": "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n  source { csv \"cards.csv\" }\n}\n",
		"main.belt":  "use deck from \"cards.belt\"\nassert deck.Cards.where(cost > 0).count() == 0\n",
	})
	if hasCode(diags, CodeUndefinedName) {
		t.Errorf("a bare column on a qualified master must not be undefined: %v", codes(diags))
	}
}

// TestBareColumnNamespaceValueShadow pins that a value of the same name as a namespace
// import shadows it: with a const named deck, deck.Cards reads the const's field, not the
// imported master, so cost in deck.Cards.where(cost > 0) is not exempted as a column but
// reported — the bare-column exemption honors the namespace shadow the way qualified
// projections do.
func TestBareColumnNamespaceValueShadow(t *testing.T) {
	diags := analyzeProject(t, map[string]string{
		"cards.belt": "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n  source { csv \"cards.csv\" }\n}\n",
		"main.belt": "use deck from \"cards.belt\"\n" +
			"pub type Box = { Cards: { id: nint } }\n" +
			"const deck: Box = { Cards: { id: 1 } }\n" +
			"assert deck.Cards.where(cost > 0) == 0\n",
	})
	if !hasCode(diags, CodeUndefinedName) {
		t.Errorf("a value shadowing the namespace makes cost an undefined name: %v", codes(diags))
	}
}
