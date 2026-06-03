package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

func analyze(src string) (*ir.Module, []diagnostic.Diagnostic) {
	return Analyze(abstract.NewDocument([]byte(src)))
}

func codes(diags []diagnostic.Diagnostic) []diagnostic.Code {
	out := make([]diagnostic.Code, len(diags))
	for i, d := range diags {
		out[i] = d.Code
	}
	return out
}

func TestAnnotatedAndUntyped(t *testing.T) {
	m, diags := analyze("const A: int32 = 1\nconst B = 0\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Consts[0].Type != ir.Int32 {
		t.Errorf("A type = %s, want int32", m.Consts[0].Type)
	}
	if m.Consts[1].Type != ir.UntypedInt {
		t.Errorf("B type = %s, want untyped int", m.Consts[1].Type)
	}
}

func TestReferenceResolutionAndTypeInheritance(t *testing.T) {
	m, diags := analyze("const A: int8 = 1\nconst B = A\nconst C = 0\nconst D = C\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	// B = A inherits A's concrete int8.
	if m.Consts[1].Type != ir.Int8 {
		t.Errorf("B type = %s, want int8", m.Consts[1].Type)
	}
	ref, ok := m.Consts[1].Value.(*ir.Reference)
	if !ok || ref.Target != m.Consts[0] {
		t.Errorf("B value = %v, want Reference -> A", m.Consts[1].Value)
	}
	// D = C inherits C's untyped int.
	if m.Consts[3].Type != ir.UntypedInt {
		t.Errorf("D type = %s, want untyped int", m.Consts[3].Type)
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

func TestUnknownType(t *testing.T) {
	m, diags := analyze("const X: notatype = 1\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeUnknownType {
		t.Fatalf("codes = %v, want [unknown_type]", got)
	}
	if m.Consts[0].Type != ir.Invalid {
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
	if m.Consts[0].Type != ir.Invalid || m.Consts[1].Type != ir.Invalid {
		t.Errorf("cyclic consts should have invalid type, got %s/%s", m.Consts[0].Type, m.Consts[1].Type)
	}
}

func TestValueEvaluation(t *testing.T) {
	m, diags := analyze("const A = 100\nconst B = A\nconst C: int64 = B\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	for i, want := range []int64{100, 100, 100} {
		ev := m.Consts[i].Eval
		if ev == nil || ev.Kind != ir.ConstInt || ev.Int.Int64() != want {
			t.Errorf("const %d eval = %v, want %d", i, ev, want)
		}
	}
}

func TestConstantOverflow(t *testing.T) {
	_, diags := analyze("const X: int8 = 1000\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func TestUntypedDoesNotOverflow(t *testing.T) {
	// An untyped constant is arbitrary precision; only a concrete type triggers
	// the range check.
	_, diags := analyze("const X = 99999999999999999999999999\n")
	if len(diags) != 0 {
		t.Fatalf("untyped constant should not overflow: %v", diags)
	}
}

func TestOverflowThroughReference(t *testing.T) {
	_, diags := analyze("const A = 1000\nconst B: int8 = A\n")
	if got := codes(diags); len(got) != 1 || got[0] != CodeConstantOverflow {
		t.Fatalf("codes = %v, want [constant_overflow]", got)
	}
}

func hasCode(diags []diagnostic.Diagnostic, code diagnostic.Code) bool {
	for _, c := range codes(diags) {
		if c == code {
			return true
		}
	}
	return false
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

func TestDivisionByZero(t *testing.T) {
	for _, src := range []string{
		"const x = 1 / 0\n",
		"const x = 1 % 0\n",
		"const z = 0\nconst x = 1 / z\n", // zero through a reference
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeDivisionByZero) {
			t.Errorf("%q: want division_by_zero, got %v", src, codes(diags))
		}
	}
	if _, diags := analyze("const x = 1 / 2\n"); hasCode(diags, CodeDivisionByZero) {
		t.Error("1 / 2 should not be division by zero")
	}
}

func TestAnnotationMismatch(t *testing.T) {
	for _, src := range []string{
		"const x: int8 = true\n",
		"const x: bool = 1\n",
		"const x: bool = 1 + 2\n",
	} {
		_, diags := analyze(src)
		if !hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: want type_mismatch, got %v", src, codes(diags))
		}
	}
	for _, src := range []string{
		"const x: int8 = 1 + 2\n",
		"const x: bool = true && false\n",
		"const x: bool = 1 < 2\n",
	} {
		if _, diags := analyze(src); hasCode(diags, CodeTypeMismatch) {
			t.Errorf("%q: unexpected type_mismatch %v", src, codes(diags))
		}
	}
}

func TestDiagnosticMessages(t *testing.T) {
	cases := []struct{ src, code, message string }{
		{"const x = 1 && 2\n", string(CodeInvalidOperation), "cannot apply method anan to untyped int, untyped int"},
		{"const x = 1 / 0\n", string(CodeDivisionByZero), "division by zero"},
		{"const x: int8 = true\n", string(CodeTypeMismatch), "cannot use untyped bool as int8"},
	}
	for _, tc := range cases {
		_, diags := analyze(tc.src)
		var msg string
		for _, d := range diags {
			if string(d.Code) == tc.code {
				msg = d.Message
			}
		}
		if msg != tc.message {
			t.Errorf("%q: message = %q, want %q", tc.src, msg, tc.message)
		}
	}
}
