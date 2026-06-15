package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestValidateAllSelfReported pins that an explicit self in a validate all clause
// is reported — self is not available, since the subject is the table, not a row —
// rather than panicking the checker. Regression: the all scope left Self nil, which
// the value leaf returned and a downstream type read dereferenced into a panic.
func TestValidateAllSelfReported(t *testing.T) {
	src := "master M {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert self.id > 0\n    }\n  }\n" +
		"}\n"
	_, diags := analyze(src) // must not panic
	if !hasCode(diags, diagnostic.Code("belt.semantic.self_outside_method")) {
		t.Fatalf("want self_outside_method for self in a validate all clause, got %v", codes(diags))
	}
}

// TestValidateAllCheckWrittenBack pins that a validate all check's value graph is
// annotated by the write-back: the resolved condition carries its settled bool
// type. Regression: AllChecks were skipped by the write-back, so a condition kept
// untyped nodes and unresolved overloads, and the data-layer fold reported false
// failures.
func TestValidateAllCheckWrittenBack(t *testing.T) {
	src := "master M {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert count < 3\n    }\n  }\n" +
		"}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	def := masterDef(m, "M")
	if def == nil || len(def.Master.AllChecks) != 1 {
		t.Fatalf("want one all check on master M")
	}
	cond := def.Master.AllChecks[0].Cond
	if got := ir.TypeOf(cond); got == nil || got.String() != "bool" {
		t.Errorf("all-check condition type = %v, want bool (write-back must annotate AllChecks)", got)
	}
}

// TestValidateAllCountNotCallable pins that count used as a call (count()) is
// reported, not silently treated as a resolved reference and left to fail the
// table at run time. count is the relation's row count, not a function.
func TestValidateAllCountNotCallable(t *testing.T) {
	src := "master M {\n" +
		"  record { id: int }\n" +
		"  primary id\n" +
		"  validate {\n    all {\n      assert count() < 3\n    }\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want a diagnostic for count() — count is not callable — got none")
	}
}
