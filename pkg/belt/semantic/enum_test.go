package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// enumDef returns the resolved enum definition of the given name in the module,
// or nil.
func enumDef(m *ir.Module) *ir.TypeDef {
	for _, t := range m.Types {
		if t.Name == "E" && t.Enum != nil {
			return t
		}
	}
	return nil
}

// memberValues renders an enum's members as "Name=value" pairs in declaration
// order, for asserting the value rules.
func memberValues(def *ir.TypeDef) string {
	parts := make([]string, len(def.Enum.Members))
	for i, m := range def.Enum.Members {
		parts[i] = m.Name + "=" + m.Value.String()
	}
	return strings.Join(parts, ",")
}

func TestEnumValueRules(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"auto from zero", "enum E {\n  A, B, C\n}\n", "A=0,B=1,C=2"},
		{"explicit then continue", "enum E: sbyte {\n  A = 5\n  B\n}\n", "A=5,B=6"},
		{"signed negative", "enum E: sbyte {\n  A = -1\n  B\n}\n", "A=-1,B=0"},
		{"explicit base values", "enum E: byte {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n", "Common=1,Rare=2,Legend=10"},
		{"string default to name", "enum E: string {\n  Ja\n  En = \"en-US\"\n}\n", `Ja="Ja",En="en-US"`},
		{"const reference", "const Base: sbyte = 4\nenum E: sbyte {\n  A = Base\n  B\n}\n", "A=4,B=5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			def := enumDef(m)
			if def == nil {
				t.Fatalf("enum E not resolved: %+v", m.Types)
			}
			if got := memberValues(def); got != tc.want {
				t.Errorf("members = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEnumBaseDefaultsToInt(t *testing.T) {
	m, diags := analyze("enum E {\n  A\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if def := enumDef(m); def == nil || def.Enum.Base != "nint" {
		t.Fatalf("base = %v, want nint", def)
	}
}

func TestEnumComparisonMethods(t *testing.T) {
	m, diags := analyze("enum E {\n  A, B\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	def := enumDef(m)
	want := map[string]bool{"eql": true, "neq": true, "lt": true, "lteq": true, "gt": true, "gteq": true}
	got := map[string]bool{}
	for _, mth := range def.Methods {
		got[mth.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("enum is missing comparison method %q", name)
		}
	}
}

func TestEnumDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{"duplicate member", "enum E {\n  A, A\n}\n", CodeDuplicateEnumMember},
		{"bool base", "enum E: bool {\n  A\n}\n", CodeInvalidEnumBaseType},
		{"user-type base", "type T = sbyte\nenum E: T {\n  A\n}\n", CodeInvalidEnumBaseType},
		{"byte overflow", "enum E: byte {\n  A = 256\n}\n", CodeConstantOverflow},
		{"nuint negative", "enum E: nuint {\n  A = -1\n}\n", CodeConstantOverflow},
		{"explicit dup value", "enum E {\n  A = 1, B = 1\n}\n", CodeDuplicateEnumValue},
		{"implicit dup value", "enum E {\n  A = 1, B, C = 1\n}\n", CodeDuplicateEnumValue},
		{"string default dup", "enum E: string {\n  Common\n  X = \"Common\"\n}\n", CodeDuplicateEnumValue},
		{"nint base string value", "enum E: sbyte {\n  A = \"x\"\n}\n", CodeTypeMismatch},
		{"member operator error", "enum E {\n  A = \"x\" * 2\n}\n", CodeInvalidOperation},
		{"member ternary mismatch", "enum E {\n  A = true ? 1 : \"s\"\n}\n", CodeTernaryBranchMismatch},
		{"unknown qualified member", "enum R {\n  A\n}\nconst x: R = R.Bogus\n", CodeUnknownEnumMember},
		{"unknown bare member", "enum R {\n  A\n}\nconst y: R = Bogus\n", CodeUnknownEnumMember},
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

// TestEnumPoisonedValueWithheld pins that a member whose initializer does not type
// has its folded value withheld, so it does not feed the duplicate-value check: a
// mismatched ternary that folds to 1 beside a member b = 1 reports the type error but
// not a spurious duplicate, because the poisoned member's value is withheld.
func TestEnumPoisonedValueWithheld(t *testing.T) {
	src := "enum E {\n  a = true ? 1 : \"s\"\n  b = 1\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTernaryBranchMismatch) {
		t.Errorf("the poisoned member's type error should be reported: %v", codes(diags))
	}
	if hasCode(diags, CodeDuplicateEnumValue) {
		t.Errorf("the poisoned member's withheld value must not trip the duplicate-value check: %v", codes(diags))
	}
}

// TestEnumValueErrorWithheld pins that a member whose value fails the base — a
// mismatched kind or an out-of-range value — is poisoned too, not only one whose
// expression draws an operator error: two members both rejected by the base do not
// then collide as a duplicate value.
func TestEnumValueErrorWithheld(t *testing.T) {
	if _, diags := analyze("enum E {\n  A = \"x\"\n  B = \"x\"\n}\n"); hasCode(diags, CodeDuplicateEnumValue) {
		t.Errorf("base-mismatched members must be withheld, not duplicate-checked: %v", codes(diags))
	}
	if _, diags := analyze("enum E: byte {\n  A = 256\n  B = 256\n}\n"); hasCode(diags, CodeDuplicateEnumValue) {
		t.Errorf("overflowing members must be withheld, not duplicate-checked: %v", codes(diags))
	}
}

// TestEnumPoisonResetsAutoNumbering pins that a poisoned member does not advance the
// auto-numbering counter: A = true ? 5 : "s" is withheld, so the implicit B continues
// from before A (0), not from the rejected 5, and a later C = 6 is not a duplicate.
func TestEnumPoisonResetsAutoNumbering(t *testing.T) {
	src := "enum E {\n  A = true ? 5 : \"s\"\n  B\n  C = 6\n}\n"
	if _, diags := analyze(src); hasCode(diags, CodeDuplicateEnumValue) {
		t.Errorf("a poisoned value must not advance auto-numbering into a later member: %v", codes(diags))
	}
}

// TestEnumNestedErrorWithheld pins that a member is poisoned by a type error nested
// under a valid outer type — a function literal with an ill-typed body that still
// returns the base type, called — so the poisoning keys off a reported diagnostic, not
// the outer expression's type. The member's value is withheld, so a sibling b = 1 is
// not reported as a duplicate.
func TestEnumNestedErrorWithheld(t *testing.T) {
	src := "enum E {\n  a = (fn(): nint {\n    let x: nint = \"s\"\n    return 1\n  })()\n  b = 1\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Errorf("the nested type error should be reported: %v", codes(diags))
	}
	if hasCode(diags, CodeDuplicateEnumValue) {
		t.Errorf("a member with a nested type error must be withheld, not duplicate-checked: %v", codes(diags))
	}
}

// TestEnumConstReferenceValueKept pins that a valid member referencing a constant is
// not withheld: A = Base (Base a sbyte constant) keeps its value, since the type
// check runs in the reporting pass where the constant is resolved — a member that
// types cleanly is never poisoned.
func TestEnumConstReferenceValueKept(t *testing.T) {
	src := "const Base: sbyte = 4\nenum E: sbyte {\n  A = Base\n  B\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("a const-reference member should settle clean: %v", codes(diags))
	}
	def := enumDef(m)
	if def.Enum.Members[0].Value == nil || def.Enum.Members[0].Value.String() != "4" {
		t.Errorf("A = Base should keep value 4, got %v", def.Enum.Members[0].Value)
	}
}

func TestEnumComparisonTypeErrors(t *testing.T) {
	// Comparing two different enums, or an enum against its base type, is a
	// type error — the comparison methods take the same enum (self).
	bad := []struct {
		name string
		src  string
	}{
		{"different enums", "enum A {\n  X\n}\nenum B {\n  Y\n}\nassert A.X == B.Y\n"},
		{"enum vs base", "enum R: sbyte {\n  X = 1\n}\nassert R.X == 1\n"},
		{"base arithmetic", "enum R: sbyte {\n  X = 1\n}\nconst y: R = R.X + 1\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if len(diags) == 0 {
				t.Errorf("src %q: want a type error, got none", tc.src)
			}
		})
	}
}

func TestEnumComparisonFolds(t *testing.T) {
	// The comparisons fold at compile time, so the assertions hold.
	good := []string{
		"enum R: byte {\n  Common = 1\n  Legend = 10\n}\nassert R.Common != R.Legend\n",
		"enum R: byte {\n  Common = 1\n  Legend = 10\n}\nassert R.Common < R.Legend\n",
		// Ordering is by base value, not declaration order: B (1) < A (5).
		"enum E: sbyte {\n  A = 5\n  B = 1\n}\nassert E.B < E.A\n",
		// String base compares lexicographically.
		"enum L: string {\n  Ja = \"Ja\"\n  Zz = \"Zz\"\n}\nassert L.Ja < L.Zz\n",
	}
	for _, src := range good {
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Errorf("src %q: unexpected diagnostics %v", src, codes(diags))
		}
	}
}

func TestEnumBareMemberNeedsExpectation(t *testing.T) {
	// A bare member with an enum annotation resolves; without one it is undefined.
	if _, diags := analyze("enum R {\n  A\n}\nconst x: R = A\n"); len(diags) != 0 {
		t.Errorf("annotated bare member: unexpected diagnostics %v", codes(diags))
	}
	if _, diags := analyze("enum R {\n  A\n}\nconst x = A\n"); !hasCode(diags, CodeUndefinedName) {
		t.Errorf("unannotated bare member: want undefined_name, got %v", codes(diags))
	}
}

// TestEnumBareMemberUnderUnionExpectation checks a bare member resolves under a
// union annotation that carries the enum (const x: R | error = Legend) exactly
// as it does under the bare enum — closing the gap where the union case fell to
// undefined_name. A name no enum in the union declares stays an
// unknown_enum_member, the same as the direct-enum case.
func TestEnumBareMemberUnderUnionExpectation(t *testing.T) {
	src := "enum R {\n  Common\n  Legend\n}\nconst x: R | error = Legend\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("bare member under R | error: unexpected diagnostics %v", codes(diags))
	}
	x := constOf(m, "x")
	if x == nil || x.Type.String() != "R | error" {
		t.Fatalf("x type = %v, want R | error", x)
	}
	if x.Eval == nil || x.Eval.String() != "R.Legend" {
		t.Errorf("x eval = %v, want R.Legend (the bare member folds)", x.Eval)
	}

	// A name no enum in the union declares is reported, not silently accepted.
	if _, diags := analyze("enum R {\n  Common\n}\nconst x: R | error = Nope\n"); !hasCode(diags, CodeUnknownEnumMember) {
		t.Errorf("non-member under R | error: want unknown_enum_member, got %v", codes(diags))
	}
}
