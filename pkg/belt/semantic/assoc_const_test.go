package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestAssocConstUserType checks a user type's associated constants: they resolve
// through TypeName.Name, fold to their declared value, and a const may be
// annotated with the owning type.
func TestAssocConstUserType(t *testing.T) {
	src := "pub type Level = sbyte impl {\n  pub const Max = 100\n  pub const Min = 0\n}\n" +
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
	src := "pub type Bits = int impl {\n  pub const Width: int = 32\n}\nconst W = Bits.Width\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if w := m.Consts[0]; w.Eval == nil || w.Eval.String() != "32" || w.Type.String() != "int" {
		t.Errorf("W = %v (type %s), want 32 (int)", w.Eval, w.Type)
	}
}

// TestAssocConstInitializerTypeChecked pins that an associated constant's
// initializer is type-checked: an operator applied to mismatched operands and a
// method call with no matching overload are reported, the same as in a top-level
// constant. These positions were folded but never typed before.
func TestAssocConstInitializerTypeChecked(t *testing.T) {
	op := "pub type T = nint impl {\n  pub const X: nint = \"abc\" * 2\n}\n"
	if _, diags := analyze(op); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("an operator type error in an assoc const initializer should be reported: %v", codes(diags))
	}
	overload := "pub type T = nint impl {\n  pub fn m(n: nint): self {\n    return self\n  }\n  pub const X: T = T(0).m(\"s\")\n}\n"
	if _, diags := analyze(overload); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("a no-overload method call in an assoc const initializer should be reported: %v", codes(diags))
	}
}

// TestEnumMemberInitializerTypeChecked pins the enum-member twin: an operator type
// error in an enum member's initializer is reported, the position now typed.
func TestEnumMemberInitializerTypeChecked(t *testing.T) {
	src := "enum E {\n  a = \"x\" * 2\n}\n"
	if _, diags := analyze(src); !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("an operator type error in an enum member initializer should be reported: %v", codes(diags))
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
		{"unknown builtin const", "const X = sbyte.Bogus\n", CodeUnknownAssociatedConst},
		{"unknown user const", "type L = sbyte impl {\n  const Max = 1\n}\nconst X = L.Bogus\n", CodeUnknownAssociatedConst},
		// A user `= builtin` constant is a builtin-surface violation at its
		// declaration site (the old no_bound was consolidated into it: a user
		// file may not write `= builtin` at all).
		{"user builtin const", "type I = builtin impl {\n  pub const Max = builtin\n}\n", CodeBuiltinOutsideBuiltin},
		// A duplicate associated-constant name keeps the first and reports.
		{"duplicate const", "type L = sbyte impl {\n  const Max = 1\n  const Max = 2\n}\n", CodeDuplicateDeclaration},
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
	src := "type Lvl = sbyte impl {\n  pub const Max = 100\n}\n" +
		"const Five: sbyte = 5\nconst X = Five.Max\n"
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

// TestAssocConstCrossTypeFolds pins gap (b) of the fold-totality plan: an
// associated constant whose initializer reads another type's member — an enum
// member here — folds, and so does a top-level reader of it. The fold runs
// after every type and enum of the file has resolved (resolveTypes' fourth
// pass), so declaration order does not decide foldability.
func TestAssocConstCrossTypeFolds(t *testing.T) {
	src := "pub enum Element {\n  Fire, Water\n}\n" +
		"pub type Config = string impl {\n  pub const Default: Element = Element.Fire\n}\n" +
		"const D = Config.Default\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	var config *ir.TypeDef
	for _, def := range m.Types {
		if def.Name == "Config" {
			config = def
		}
	}
	if config == nil || len(config.Consts) != 1 {
		t.Fatalf("Config not resolved with its constant")
	}
	if v := config.Consts[0].Value; v == nil || v.Kind != ir.ConstEnum || v.EnumIndex != 0 {
		t.Errorf("Config.Default = %v, want Element.Fire", v)
	}
	if v := m.Consts[0].Eval; v == nil || v.Kind != ir.ConstEnum || v.EnumIndex != 0 {
		t.Errorf("const D = %v, want Element.Fire (the Type.Name reader folds too)", v)
	}
}

// TestAssocConstReferenceDiagnostics pins that an associated-constant
// initializer reports its reference problems — they used to be entirely
// silent: an undefined name, an unknown namespace-less member, and a stray
// self.
func TestAssocConstReferenceDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"undefined name", "pub type C = string impl {\n  pub const D = bogus\n}\n", CodeUndefinedName},
		{"unknown enum member", "pub enum E {\n  A\n}\npub type C = string impl {\n  pub const D: E = Bogus\n}\n", CodeUnknownEnumMember},
		{"self in initializer", "pub type C = string impl {\n  pub const D = self\n}\n", CodeSelfOutsideMethod},
		{"enum impl undefined", "pub enum E {\n  A\n} impl {\n  pub const D = bogus\n}\n", CodeUndefinedName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Errorf("want %s, got %v", tc.want, codes(diags))
			}
		})
	}
}
