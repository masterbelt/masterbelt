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

func TestRefinementUndefinedNameReported(t *testing.T) {
	// A where-clause referencing a name that resolves to nothing once typed to
	// Invalid and was dropped without a word: the refinement silently never
	// applied, so a value the author meant to constrain passed unchecked. The
	// undefined reference is reported, once, at the declaration, exactly as a
	// const initializer's would be.
	m, diags := analyze("pub type Strong = int where self >= UNDEFINED\nconst c: Strong = 5\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUndefinedName {
		t.Fatalf("codes = %v, want [undefined_name]", got)
	}
	if !strings.Contains(diags[0].Message, "UNDEFINED") {
		t.Errorf("message %q does not name the undefined reference", diags[0].Message)
	}
	// The unusable predicate stays off the definition — no half-applied refinement.
	if m.Types[0].Where != nil {
		t.Error("Strong.Where != nil, want nil for an unresolved predicate")
	}
}

func TestRefinementConstReferenceReported(t *testing.T) {
	// A predicate may read only self, literals, and self's own methods — never a
	// top-level constant. Referencing one once typed to Invalid and was dropped
	// silently, leaving the refinement unenforced. It is now reported as not a
	// usable compile-time predicate, at the declaration, and the definition keeps
	// no half-formed where-clause.
	m, diags := analyze("const MIN: int = 10\npub type Strong = int where self >= MIN\nconst c: Strong = 5\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementNotConstant {
		t.Fatalf("codes = %v, want [refinement_not_constant]", got)
	}
	if m.Types[0].Where != nil {
		t.Error("Strong.Where != nil, want nil for a constant-referencing predicate")
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

// TestStringRefinement checks a string-based
// refinement (the "literal union" idiom), which was impossible because `self == "north"`
// did not type-check — the self-typed operand never unified with the string
// literal. Now the predicate validates, a member of the value set passes, and a
// non-member is a refinement_violation with the value, type, and predicate quoted.
func TestStringRefinement(t *testing.T) {
	def := "pub type Direction = string where self == \"north\" || self == \"south\"\n"

	// The predicate validates and rides the definition.
	m, diags := analyze(def + "const D: Direction = \"north\"\n")
	if len(diags) != 0 {
		t.Fatalf("a valid member should pass, got %v", codes(diags))
	}
	if m.Types[0].Where == nil {
		t.Error("Direction.Where = nil, want the validated string predicate")
	}

	// A value outside the set is a refinement_violation.
	_, diags = analyze(def + "const W: Direction = \"weast\"\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeRefinementViolation {
		t.Fatalf("\"weast\" should violate the refinement, got %v", got)
	}
	for _, want := range []string{"weast", "Direction", "self == \"north\""} {
		if !strings.Contains(diags[0].Message, want) {
			t.Errorf("message %q does not mention %q", diags[0].Message, want)
		}
	}
}

// TestBoolRefinement checks a boolean base refines the same way: the predicate
// folds against the value, the satisfying constant passes, the other violates.
func TestBoolRefinement(t *testing.T) {
	def := "pub type Truthy = bool where self == true\n"
	if _, diags := analyze(def + "const T: Truthy = true\n"); len(diags) != 0 {
		t.Errorf("true should satisfy self == true, got %v", codes(diags))
	}
	if _, diags := analyze(def + "const F: Truthy = false\n"); len(codes(diags)) != 1 || codes(diags)[0] != CodeRefinementViolation {
		t.Errorf("false should violate self == true, got %v", codes(diags))
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
