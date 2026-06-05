package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

const rarityEnum = "pub enum Rarity { Common, Rare, Legend }\n"

// hasCodeSwitch reports whether diags carry code (a local alias avoids clashing
// with the other test files' helpers).
func hasCodeSwitch(diags []diagnostic.Diagnostic, code diagnostic.Code) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

// TestSwitchExhaustiveEnumOK checks that a switch covering every enum member
// analyzes cleanly — no missing_return (return analysis sees the switch always
// returns) and no non_exhaustive_switch.
func TestSwitchExhaustiveEnumOK(t *testing.T) {
	_, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    Common -> return \"w\"\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestSwitchNonExhaustiveEnum checks that a switch missing an enum member is
// reported, and the missing member is named.
func TestSwitchNonExhaustiveEnum(t *testing.T) {
	_, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    Common -> return \"w\"\n    Rare -> return \"b\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeNonExhaustiveSwitch) {
		t.Fatalf("want non_exhaustive_switch, got %v", codes(diags))
	}
	var msg string
	for _, d := range diags {
		if d.Code == CodeNonExhaustiveSwitch {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "Rarity.Legend") {
		t.Errorf("message should name the missing member Rarity.Legend, got %q", msg)
	}
}

// TestSwitchScalarRequiresWildcard checks that a scalar switch without a "_"
// arm is non-exhaustive (its domain is unbounded).
func TestSwitchScalarRequiresWildcard(t *testing.T) {
	_, diags := analyze("pub fn g(n: int): string {\n  switch n {\n    0 -> return \"z\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeNonExhaustiveSwitch) {
		t.Fatalf("want non_exhaustive_switch for a wildcard-less scalar switch, got %v", codes(diags))
	}
}

// TestSwitchScalarWithWildcardOK checks that a scalar switch with a "_" arm is
// exhaustive and returns.
func TestSwitchScalarWithWildcardOK(t *testing.T) {
	_, diags := analyze("pub fn g(n: int): string {\n  switch n {\n    0 -> return \"z\"\n    1, 2, 3 -> return \"l\"\n    _ -> return \"h\"\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestSwitchDuplicateArm checks that two arms naming the same value are
// reported once, on the second.
func TestSwitchDuplicateArm(t *testing.T) {
	_, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    Common -> return \"w\"\n    Common -> return \"x\"\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeDuplicateSwitchArm) {
		t.Fatalf("want duplicate_switch_arm, got %v", codes(diags))
	}
	// A pure duplicate is not also reported as unreachable_arm.
	if hasCodeSwitch(diags, CodeUnreachableArm) {
		t.Errorf("a duplicate arm should not also be unreachable_arm: %v", codes(diags))
	}
}

// TestSwitchArmValueTypeMismatch checks that an arm value of the wrong type is
// reported against the scrutinee type.
func TestSwitchArmValueTypeMismatch(t *testing.T) {
	_, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    5 -> return \"w\"\n    _ -> return \"x\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeArmValueTypeMismatch) {
		t.Fatalf("want arm_value_type_mismatch, got %v", codes(diags))
	}
}

// TestSwitchUnreachableAfterWildcard checks that an arm written after the
// wildcard is reported unreachable.
func TestSwitchUnreachableAfterWildcard(t *testing.T) {
	_, diags := analyze("pub fn g(n: int): string {\n  switch n {\n    0 -> return \"z\"\n    _ -> return \"h\"\n    1 -> return \"o\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeUnreachableArm) {
		t.Fatalf("want unreachable_arm, got %v", codes(diags))
	}
}

// TestSwitchMissingReturnWhenArmFallsThrough checks that an enum switch one of
// whose arms does not return leaves the function missing a return.
func TestSwitchMissingReturnWhenArmFallsThrough(t *testing.T) {
	_, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    Common -> log(r)\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n}\n")
	if !hasCodeSwitch(diags, CodeMissingReturn) {
		t.Fatalf("want missing_return when an arm falls through, got %v", codes(diags))
	}
}

// TestSwitchBareMemberResolves checks that a bare enum member in an arm value
// resolves against the scrutinee's enum (so it is not a type mismatch).
func TestSwitchBareMemberResolves(t *testing.T) {
	m, diags := analyze(rarityEnum + "pub fn color(r: Rarity): string {\n  switch r {\n    Common -> return \"w\"\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	dump := ir.Dump(m)
	if !strings.Contains(dump, "arm Rarity.Common") {
		t.Errorf("bare member Common should resolve to Rarity.Common in the IR:\n%s", dump)
	}
}

// evalOf returns the folded value of the named constant, failing the test when
// the program does not analyze cleanly or the constant does not fold.
func evalOf(t *testing.T, src, name string) *ir.Constant {
	t.Helper()
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	for _, c := range m.Consts {
		if c.Name == name {
			if c.Eval == nil {
				t.Fatalf("const %s did not fold", name)
			}
			return c.Eval
		}
	}
	t.Fatalf("const %s not found", name)
	return nil
}

// TestSwitchEvalScalarDispatch checks that a switch in a pure function folds at
// compile time by running the arm the scrutinee selects — the first match, with
// the multi-value arm and the wildcard both exercised.
func TestSwitchEvalScalarDispatch(t *testing.T) {
	src := "fn grade(n: int): string {\n  switch n {\n    0 -> return \"zero\"\n    1, 2, 3 -> return \"low\"\n    _ -> return \"high\"\n  }\n}\nconst A = grade(0)\nconst B = grade(2)\nconst C = grade(99)\n"
	for _, tc := range []struct{ name, want string }{{"A", "zero"}, {"B", "low"}, {"C", "high"}} {
		if got := evalOf(t, src, tc.name).Str; got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSwitchEvalEnumDispatch checks that an enum switch dispatches at compile
// time on the member identity, with a bare-member arm.
func TestSwitchEvalEnumDispatch(t *testing.T) {
	src := rarityEnum + "fn color(r: Rarity): string {\n  switch r {\n    Common -> return \"white\"\n    Rare -> return \"blue\"\n    Legend -> return \"gold\"\n  }\n}\nconst D = color(Rarity.Legend)\nconst E = color(Rarity.Common)\n"
	if got := evalOf(t, src, "D").Str; got != "gold" {
		t.Errorf("D = %q, want gold", got)
	}
	if got := evalOf(t, src, "E").Str; got != "white" {
		t.Errorf("E = %q, want white", got)
	}
}
