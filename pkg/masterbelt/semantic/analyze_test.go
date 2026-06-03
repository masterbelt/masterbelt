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
