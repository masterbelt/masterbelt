package semantic

import (
	"strings"
	"testing"
)

func TestRefinementSatisfied(t *testing.T) {
	m, diags := analyze("pub type Port = int where self >= 1 && self <= 65535\n" +
		"const P: Port = 8080\nconst Min: Port = 1\nconst Max: Port = 65535\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// The validated predicate rides the definition, in evaluable form.
	if m.Types[0].Where == nil {
		t.Error("Port.Where = nil, want the validated predicate")
	}
}

func TestRefinementViolation(t *testing.T) {
	// 70000 fits int32 — overflow stays silent; only the predicate catches it.
	_, diags := analyze("pub type Port = int where self >= 1 && self <= 65535\n" +
		"const Broken: Port = 70000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
	// The message quotes the value, the type, and the predicate in its
	// canonical surface form — the material a structured fix would build on.
	for _, want := range []string{"70000", "Port", "self >= 1 && self <= 65535"} {
		if !strings.Contains(diags[0].Message, want) {
			t.Errorf("message %q does not mention %q", diags[0].Message, want)
		}
	}
	// The power-assert diagram follows, indented as a block, with self bound
	// to the value — it shows which comparison rejected the constant.
	diagram := "\n  self >= 1 && self <= 65535\n" +
		"  ^    ^    ^  ^    ^\n" +
		"  |    true |  |    false\n" +
		"  70000     |  70000\n" +
		"            false"
	if !strings.Contains(diags[0].Message, diagram) {
		t.Errorf("message %q does not contain the diagram %q", diags[0].Message, diagram)
	}
}

func TestRefinementViolationNegative(t *testing.T) {
	_, diags := analyze("pub type Level = sbyte where self >= 0 && self <= 100\n" +
		"const Neg: Level = -1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
}

func TestRefinementViolationThroughReference(t *testing.T) {
	// The annotated constant's value arrives through a reference; the predicate
	// folds against the evaluated value all the same.
	_, diags := analyze("pub type Port = int where self >= 1\n" +
		"const A = 0\nconst B: Port = A\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
}

func TestRefinementOverflowSuppressesViolation(t *testing.T) {
	// A value outside the underlying range is an overflow, already outside the
	// type — the predicate is not piled on top.
	_, diags := analyze("pub type Port = int where self >= 1\n" +
		"const Huge: Port = 99999999999\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestRefinementUnannotatedConstUnchecked(t *testing.T) {
	// An unannotated constant is a plain int: no refinement applies (the MVP
	// checks annotation-driven nominal types only).
	_, diags := analyze("pub type Port = int where self >= 1 && self <= 65535\n" +
		"const x = 70000\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestRefinementNotBool(t *testing.T) {
	m, diags := analyze("pub type Bad = sbyte where self + 1\nconst c: Bad = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementNotBool {
		t.Fatalf("codes = %v, want [refinement_not_bool]", got)
	}
	// An unusable predicate stays off the definition, so the constant check
	// stayed silent (one report, at the declaration).
	if m.Types[0].Where != nil {
		t.Error("Bad.Where != nil, want nil for a non-bool predicate")
	}
}

func TestRefinementNotConstant(t *testing.T) {
	// self / 0 types as an integer division but can never fold; the predicate
	// is reported as not a compile-time predicate, once, at the declaration.
	_, diags := analyze("pub type Bad = sbyte where self / 0 == 0\nconst c: Bad = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementNotConstant {
		t.Fatalf("codes = %v, want [refinement_not_constant]", got)
	}
}

func TestRefinementBadMethod(t *testing.T) {
	// A method self's body type does not have is an operator type error — the
	// predicate's problem is reported once, as invalid_operation, and the
	// constant check stays silent.
	_, diags := analyze("pub type Bad = sbyte where self.bogus()\nconst c: Bad = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeInvalidOperation {
		t.Fatalf("codes = %v, want [invalid_operation]", got)
	}
}

func TestRefinementWithImpl(t *testing.T) {
	// A where-clause composes with an impl block; the predicate still gates
	// annotated constants.
	_, diags := analyze("pub type Pct = sbyte where self >= 0 && self <= 100 impl {\n" +
		"  pub increment(): self {\n    return self + 1\n  }\n}\n" +
		"const p: Pct = 101\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("codes = %v, want [refinement_violation]", got)
	}
}
