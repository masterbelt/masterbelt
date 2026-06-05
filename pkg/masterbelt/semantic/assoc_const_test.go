package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

// TestAssocConstUserType checks a user type's associated constants: they resolve
// through TypeName.Name, fold to their declared value, and a const may be
// annotated with the owning type.
func TestAssocConstUserType(t *testing.T) {
	src := "pub type Level = int8 impl {\n  pub const Max = 100\n  pub const Min = 0\n}\n" +
		"const Top: Level = Level.Max\n" +
		"const Bottom: Level = Level.Min\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if top := m.Consts[0]; top.Eval == nil || top.Eval.String() != "100" {
		t.Errorf("Top = %v, want 100", top.Eval)
	}
	if bot := m.Consts[1]; bot.Eval == nil || bot.Eval.String() != "0" {
		t.Errorf("Bottom = %v, want 0", bot.Eval)
	}
	// The constants live on the type definition.
	for _, td := range m.Types {
		if td.Name == "Level" {
			if len(td.Consts) != 2 {
				t.Fatalf("Level has %d consts, want 2", len(td.Consts))
			}
		}
	}
}

// TestAssocConstTypedAnnotation checks that an associated constant's own type
// annotation is honoured.
func TestAssocConstTypedAnnotation(t *testing.T) {
	src := "pub type Bits = int32 impl {\n  pub const Width: int32 = 32\n}\nconst W = Bits.Width\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if w := m.Consts[0]; w.Eval == nil || w.Eval.String() != "32" || w.Type.String() != "int32" {
		t.Errorf("W = %v (type %s), want 32 (int32)", w.Eval, w.Type)
	}
}

// TestAssocConstDiagnostics covers the new and reused error paths.
func TestAssocConstDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		// An unknown associated constant on a type.
		{"unknown builtin const", "const X = int8.Bogus\n", CodeUnknownAssociatedConst},
		{"unknown user const", "type L = int8 impl {\n  const Max = 1\n}\nconst X = L.Bogus\n", CodeUnknownAssociatedConst},
		// The arbitrary-precision integers have no bound, so a `= builtin` Max on
		// them is a no_bound error.
		{"no bound on int", "type I = builtin impl {\n  pub const Max = builtin\n}\n", CodeNoBound},
		// A duplicate associated-constant name keeps the first and reports.
		{"duplicate const", "type L = int8 impl {\n  const Max = 1\n  const Max = 2\n}\n", CodeDuplicateDeclaration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Errorf("src %q: want %s, got %v", tc.src, tc.want, codes(diags))
			}
		})
	}
}

// TestAssocConstIsTypeAccessOnly checks that an associated constant is reached
// only through the type, never through a value receiver: `x.Max` on a value is
// a field access (which an int8 value does not have), not the type's constant,
// so it does not fold to the bound.
func TestAssocConstIsTypeAccessOnly(t *testing.T) {
	src := "type Lvl = int8 impl {\n  pub const Max = 100\n}\n" +
		"const Five: int8 = 5\nconst X = Five.Max\n"
	m, _ := analyze(src)
	x := m.Consts[1]
	if x.Name != "X" {
		t.Fatalf("second const = %q, want X", x.Name)
	}
	// Five.Max is a field read on a value, not the type's associated constant, so
	// it must not fold to 100 — it has no value at all.
	if x.Eval != nil {
		t.Errorf("Five.Max folded to %v, want no value (type access only)", x.Eval)
	}
}
