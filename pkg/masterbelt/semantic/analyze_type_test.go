// This file tests name resolution and the type half of analysis: reference
// resolution and inheritance, annotation mismatches and unknown/cyclic types,
// operator type errors, and the record-literal type rules.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestReferenceResolutionAndTypeInheritance(t *testing.T) {
	m, diags := analyze("const A: sbyte = 1\nconst B = A\nconst C = 0\nconst D = C\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// B = A inherits A's concrete int8.
	if m.Consts[1].Type.String() != "sbyte" {
		t.Errorf("B type = %s, want sbyte", m.Consts[1].Type)
	}
	ref, ok := m.Consts[1].Value.(*ir.Reference)
	if !ok || ref.Target != m.Consts[0] {
		t.Errorf("B value = %v, want Reference -> A", m.Consts[1].Value)
	}
	// D = C inherits C's int.
	if m.Consts[3].Type.String() != "nint" {
		t.Errorf("D type = %s, want nint", m.Consts[3].Type)
	}
}

func TestUndefinedName(t *testing.T) {
	m, diags := analyze("const X = Missing\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUndefinedName {
		t.Fatalf("codes = %v, want [undefined_name]", got)
	}
	if m.Consts[0].Value != nil {
		t.Errorf("X value = %v, want nil (unresolved)", m.Consts[0].Value)
	}
}

func TestDuplicateDeclaration(t *testing.T) {
	_, diags := analyze("const X = 1\nconst X = 2\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateDeclaration {
		t.Fatalf("codes = %v, want [duplicate_declaration]", got)
	}
}

func TestLocalTypeAnnotation(t *testing.T) {
	// A file's own type declarations are visible to its const annotations —
	// the same universe imported types join (P-2). The annotated constant's
	// Named type points at the very TypeDef the module publishes.
	m, diags := analyze("pub type Coin = sbyte\nconst c: Coin = 1\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Coin" {
		t.Errorf("c type = %s, want Coin", m.Consts[0].Type)
	}
	named, ok := m.Consts[0].Type.(*ir.Named)
	if !ok || named.Def != m.Types[0] {
		t.Errorf("c's Named.Def = %v, want the module's Coin definition", m.Consts[0].Type)
	}
}

func TestLocalTypeAnnotationRangeChecked(t *testing.T) {
	// The named type's underlying range still applies.
	_, diags := analyze("pub type Coin = sbyte\nconst c: Coin = 1000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestUnknownType(t *testing.T) {
	m, diags := analyze("const X: notatype = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnknownType {
		t.Fatalf("codes = %v, want [unknown_type]", got)
	}
	if m.Consts[0].Type.String() != "invalid" {
		t.Errorf("X type = %s, want invalid", m.Consts[0].Type)
	}
}

func TestCyclicReference(t *testing.T) {
	m, diags := analyze("const A = B\nconst B = A\n")
	// Both declarations are on the cycle, so both are flagged.
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeCyclicReference || got[1] != CodeCyclicReference {
		t.Fatalf("codes = %v, want two cyclic_reference", got)
	}
	if m.Consts[0].Type.String() != "invalid" || m.Consts[1].Type.String() != "invalid" {
		t.Errorf("cyclic consts should have invalid type, got %s/%s", m.Consts[0].Type, m.Consts[1].Type)
	}
}

func TestStringAnnotationMismatch(t *testing.T) {
	// A string initializer under a non-string annotation (and vice versa) is a
	// type mismatch; a string under a string annotation is fine.
	for _, src := range []string{
		"const x: sbyte = \"no\"\n",
		"const x: string = 1\n",
		"const x: bool = \"no\"\n",
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: want type_mismatch, got %v", src, codes(diags))
		}
	}
	if _, diags := analyze("const x: string = \"yes\"\n"); hasCode(diags, CodeTypeMismatch) {
		t.Errorf("string = string should not mismatch, got %v", codes(diags))
	}
}

func TestOperatorTypeErrors(t *testing.T) {
	bad := []string{
		"const x = 1 && 2\n",   // logical operator on integers
		"const x = true + 1\n", // arithmetic on a boolean
		"const x = 1 < true\n", // comparison of mixed kinds
		"const x = !1\n",       // not on an integer
		"const x = -true\n",    // neg on a boolean
	}
	for _, src := range bad {
		_, diags := analyze(src)
		if !hasCode(diags, CodeInvalidOperation) {
			t.Errorf("%q: want invalid_operation, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{"const x = 1 + 2\n", "const x = true && false\n", "const x = 1 < 2\n"} {
		if _, diags := analyze(src); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, codes(diags))
		}
	}
}

func TestOperatorErrorReportedOnce(t *testing.T) {
	// The inner type error is reported once; the outer call sees an Invalid
	// operand and does not pile on.
	_, diags := analyze("const x = 1 && 2 && 3\n")
	n := 0
	for _, c := range codes(diags) {
		if c == CodeInvalidOperation {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one invalid_operation, got %d: %v", n, codes(diags))
	}
}

// TestExprStmtIsTypeChecked checks that an expression statement — a call whose
// value is discarded — is type-checked in a method and a function body, so an
// undefined method or a bad operand surfaces rather than being silently
// dropped. A valid discarded call draws no diagnostic.
func TestExprStmtIsTypeChecked(t *testing.T) {
	// A discarded call to an undefined method in a function body, before a valid
	// return: the bad statement is reported, the return is fine.
	fnBad := "pub fn f(n: sbyte): sbyte {\n  n.nonexistent()\n  return n\n}\n"
	if _, diags := analyze(fnBad); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("function expr-stmt: want invalid_operation for n.nonexistent(), got %v", codes(diags))
	}

	// The same in a method body, where self is bound.
	methodBad := "pub type Lvl = sbyte impl {\n  pub fn touch(): self {\n    self.nope()\n    return self\n  }\n}\n"
	if _, diags := analyze(methodBad); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("method expr-stmt: want invalid_operation for self.nope(), got %v", codes(diags))
	}

	// A valid discarded call (add is defined on the integer family) before a
	// valid return draws no diagnostic — the expression statement type-checks
	// clean.
	fnOK := "pub fn g(n: sbyte): sbyte {\n  n.add(1)\n  return n\n}\n"
	if _, diags := analyze(fnOK); len(diags) != 0 {
		t.Errorf("valid expr-stmt: unexpected diagnostics %v", codes(diags))
	}
}

func TestAnnotationMismatch(t *testing.T) {
	for _, src := range []string{
		"const x: sbyte = true\n",
		"const x: bool = 1\n",
		"const x: bool = 1 + 2\n",
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: want type_mismatch, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{
		"const x: sbyte = 1 + 2\n",
		"const x: bool = true && false\n",
		"const x: bool = 1 < 2\n",
	} {
		if _, diags := analyze(src); hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: unexpected type_mismatch %v", src, codes(diags))
		}
	}
}

// --- record literals ----------------------------------------------------------

const pointDecl = "pub type Point = { x: nint, y: nint }\n"

func TestRecordLiteralTypedForm(t *testing.T) {
	m, diags := analyze(pointDecl + "const O = Point{ x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Point" {
		t.Errorf("O type = %s, want Point", m.Consts[0].Type)
	}
	named, ok := m.Consts[0].Type.(*ir.Named)
	if !ok || named.Def != m.Types[0] {
		t.Errorf("O's Named.Def = %v, want the module's Point definition", m.Consts[0].Type)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("O eval = %s, want { x: 1, y: 2 }", got)
	}
}

func TestRecordLiteralInferredForm(t *testing.T) {
	// The annotation supplies the inferred form's type, exactly as it types an
	// empty collection.
	m, diags := analyze(pointDecl + "const P: Point = { x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type.String() != "Point" {
		t.Errorf("P type = %s, want Point", m.Consts[0].Type)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("P eval = %s, want { x: 1, y: 2 }", got)
	}
}

func TestRecordLiteralEmpty(t *testing.T) {
	m, diags := analyze("pub type Unit = {}\nconst U: Unit = {}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{}" {
		t.Errorf("U eval = %s, want {}", got)
	}
}

func TestRecordLiteralNormalizesFieldOrder(t *testing.T) {
	// The literal may write fields in any order; the folded constant is
	// canonical, so the two spellings evaluate identically.
	m, diags := analyze(pointDecl + "const A = Point{ y: 2, x: 1 }\nconst B = Point{ x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if a, b := m.Consts[0].Eval.String(), m.Consts[1].Eval.String(); a != b {
		t.Errorf("evals differ: %s vs %s", a, b)
	}
}

func TestRecordLiteralNested(t *testing.T) {
	m, diags := analyze(pointDecl +
		"pub type Item = { id: nint, name: string, pos: Point }\n" +
		"const Sword = Item{ id: 1, name: \"Sword\", pos: { x: 0, y: 0 } }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ id: 1, name: \"Sword\", pos: { x: 0, y: 0 } }" {
		t.Errorf("Sword eval = %s", got)
	}
}

func TestRecordLiteralDatetimeDurationFields(t *testing.T) {
	m, diags := analyze("pub type Event = { at: datetime, cooldown: duration }\n" +
		"const E = Event{ at: D2026-06-05T00:00:00.000Z, cooldown: 90m }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ at: D2026-06-05T00:00:00.000Z, cooldown: 1h30m }" {
		t.Errorf("E eval = %s", got)
	}
}

func TestRecordLiteralDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"missing field", pointDecl + "const A = Point{ x: 1 }\n", CodeMissingField},
		{"unknown field", pointDecl + "const B = Point{ x: 1, y: 2, z: 3 }\n", CodeUnknownField},
		{"missing field through annotation", pointDecl + "const C: Point = { x: 1 }\n", CodeMissingField},
		{"no expectation", "const D = { x: 1, y: 2 }\n", CodeUninferableRecord},
		{"unknown type name", "const E = Bogus{ x: 1 }\n", CodeUnknownType},
		{"not a record", "pub type Coin = sbyte\nconst F = Coin{ x: 1 }\n", CodeNotARecord},
		{"non-record expectation", "const G: nint = { x: 1 }\n", CodeTypeMismatch},
		{"wrong record type", pointDecl + "pub type Unit = {}\nconst H: Unit = Point{ x: 1, y: 2 }\n", CodeTypeMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if got := codes(diags); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("codes = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestRecordLiteralMissingFieldsReportedEach(t *testing.T) {
	_, diags := analyze(pointDecl + "const A = Point{}\n")
	got := codes(diags)
	if len(got) != 2 || got[0] != CodeMissingField || got[1] != CodeMissingField {
		t.Fatalf("codes = %v, want two missing_field", got)
	}
}

func TestRecordFieldRangeChecked(t *testing.T) {
	// A field value adapts to the field's declared type and is range-checked
	// against it — with and without an annotation on the constant.
	for _, src := range []string{
		"pub type B = { v: sbyte }\nconst X = B{ v: 1000 }\n",
		"pub type B = { v: sbyte }\nconst X: B = { v: 1000 }\n",
	} {
		_, diags := analyze(src)
		if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
			t.Fatalf("%q: codes = %v, want [constant_overflow]", src, got)
		}
	}
}

func TestRecordFieldTypeMismatch(t *testing.T) {
	_, diags := analyze(pointDecl + "const A = Point{ x: \"a\", y: 2 }\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeTypeMismatch {
		t.Fatalf("codes = %v, want [type_mismatch]", got)
	}
}

func TestRecordExpectationReachesCollectionElements(t *testing.T) {
	// The annotation's element type reaches each element, so an inferred
	// record literal works inside a list.
	m, diags := analyze(pointDecl + "const Path: list<Point> = [{ x: 0, y: 0 }, Point{ x: 1, y: 1 }]\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "[{ x: 0, y: 0 }, { x: 1, y: 1 }]" {
		t.Errorf("Path eval = %s", got)
	}
}

func TestRecordAnnotationFailureSuppressesUninferable(t *testing.T) {
	// The broken annotation is the problem; had it resolved it would have
	// typed the literal, so only unknown_type reports.
	_, diags := analyze("const X: Bogus = { x: 1 }\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnknownType {
		t.Fatalf("codes = %v, want [unknown_type]", got)
	}
}

func TestRecordLiteralInMethodBodyReturn(t *testing.T) {
	// The method's declared result type reaches an inferred record literal in
	// a return, through the same checking walk the const path uses.
	_, diags := analyze("pub type Point = { x: nint, y: nint } impl {\n" +
		"  pub origin(): Point {\n    return { x: 0, y: 0 }\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestRecordLiteralStructuralAnnotation(t *testing.T) {
	// A structural record annotation works exactly like a named one.
	m, diags := analyze("const P: { x: nint, y: nint } = { x: 1, y: 2 }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := m.Consts[0].Eval.String(); got != "{ x: 1, y: 2 }" {
		t.Errorf("P eval = %s", got)
	}
}
