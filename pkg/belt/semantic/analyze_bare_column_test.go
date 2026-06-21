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
