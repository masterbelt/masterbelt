package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// enumDef returns the resolved enum definition of the given name in the module,
// or nil.
func enumDef(m *ir.Module, name string) *ir.TypeDef {
	for _, t := range m.Types {
		if t.Name == name && t.Enum != nil {
			return t
		}
	}
	return nil
}

// memberValues renders an enum's members as "Name=value" pairs in declaration
// order, for asserting the §3.5 value rules.
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
		{"explicit then continue", "enum E: int8 {\n  A = 5\n  B\n}\n", "A=5,B=6"},
		{"signed negative", "enum E: int8 {\n  A = -1\n  B\n}\n", "A=-1,B=0"},
		{"explicit base values", "enum E: uint8 {\n  Common = 1\n  Rare = 2\n  Legend = 10\n}\n", "Common=1,Rare=2,Legend=10"},
		{"string default to name", "enum E: string {\n  Ja\n  En = \"en-US\"\n}\n", `Ja="Ja",En="en-US"`},
		{"const reference", "const Base: int8 = 4\nenum E: int8 {\n  A = Base\n  B\n}\n", "A=4,B=5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := analyze(tc.src)
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			def := enumDef(m, "E")
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
	if def := enumDef(m, "E"); def == nil || def.Enum.Base != "int" {
		t.Fatalf("base = %v, want int", def)
	}
}

func TestEnumComparisonMethods(t *testing.T) {
	m, diags := analyze("enum E {\n  A, B\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	def := enumDef(m, "E")
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
		{"user-type base", "type T = int8\nenum E: T {\n  A\n}\n", CodeInvalidEnumBaseType},
		{"uint8 overflow", "enum E: uint8 {\n  A = 256\n}\n", CodeConstantOverflow},
		{"uint negative", "enum E: uint {\n  A = -1\n}\n", CodeConstantOverflow},
		{"explicit dup value", "enum E {\n  A = 1, B = 1\n}\n", CodeDuplicateEnumValue},
		{"implicit dup value", "enum E {\n  A = 1, B, C = 1\n}\n", CodeDuplicateEnumValue},
		{"string default dup", "enum E: string {\n  Common\n  X = \"Common\"\n}\n", CodeDuplicateEnumValue},
		{"int base string value", "enum E: int8 {\n  A = \"x\"\n}\n", CodeTypeMismatch},
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

func TestEnumComparisonTypeErrors(t *testing.T) {
	// Comparing two different enums, or an enum against its base type, is a
	// type error — the comparison methods take the same enum (self).
	bad := []struct {
		name string
		src  string
	}{
		{"different enums", "enum A {\n  X\n}\nenum B {\n  Y\n}\nassert A.X == B.Y\n"},
		{"enum vs base", "enum R: int8 {\n  X = 1\n}\nassert R.X == 1\n"},
		{"base arithmetic", "enum R: int8 {\n  X = 1\n}\nconst y: R = R.X + 1\n"},
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
		"enum R: uint8 {\n  Common = 1\n  Legend = 10\n}\nassert R.Common != R.Legend\n",
		"enum R: uint8 {\n  Common = 1\n  Legend = 10\n}\nassert R.Common < R.Legend\n",
		// Ordering is by base value, not declaration order: B (1) < A (5).
		"enum E: int8 {\n  A = 5\n  B = 1\n}\nassert E.B < E.A\n",
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
