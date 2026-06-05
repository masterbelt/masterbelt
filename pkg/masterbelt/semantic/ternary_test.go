package semantic

import (
	"testing"
)

// TestTernaryConditionNotBool checks that a non-bool ternary condition is
// reported as ternary_condition_not_bool.
func TestTernaryConditionNotBool(t *testing.T) {
	_, diags := analyze("const X = 1 ? 2 : 3\n")
	if !hasCodeSwitch(diags, CodeTernaryConditionNotBool) {
		t.Fatalf("want ternary_condition_not_bool, got %v", codes(diags))
	}
}

// TestTernaryConditionBoolOK checks that a bool condition (a comparison)
// analyzes cleanly and yields the branches' type.
func TestTernaryConditionBoolOK(t *testing.T) {
	_, diags := analyze("const X = 1 > 0 ? 2 : 3\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestTernaryBranchMismatch checks that two branches whose types do not unify
// are reported as ternary_branch_mismatch.
func TestTernaryBranchMismatch(t *testing.T) {
	_, diags := analyze("const X = true ? 1 : \"s\"\n")
	if !hasCodeSwitch(diags, CodeTernaryBranchMismatch) {
		t.Fatalf("want ternary_branch_mismatch, got %v", codes(diags))
	}
}

// TestTernaryBranchesUnify checks that two branches of the same kind unify
// cleanly — an int branch beside a default-int literal stays int.
func TestTernaryBranchesUnify(t *testing.T) {
	_, diags := analyze("const X = true ? 1 : 2\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestTernaryType checks that a ternary's type is its branches' unified type:
// int for two ints, string for two strings, bool for two bools.
func TestTernaryType(t *testing.T) {
	for _, tc := range []struct{ src, name, want string }{
		{"const X = true ? 1 : 2\n", "X", "int"},
		{"const Y = true ? \"a\" : \"b\"\n", "Y", "string"},
		{"const Z = true ? false : true\n", "Z", "bool"},
	} {
		m, diags := analyze(tc.src)
		if len(diags) != 0 {
			t.Fatalf("%q: unexpected diagnostics: %v", tc.src, codes(diags))
		}
		for _, c := range m.Consts {
			if c.Name == tc.name {
				if got := c.Type.String(); got != tc.want {
					t.Errorf("%q: type = %s, want %s", tc.src, got, tc.want)
				}
			}
		}
	}
}

// TestTernaryEvalDispatch checks that a ternary folds at compile time by
// evaluating only the taken branch: a true condition yields the then-branch, a
// false condition the else-branch.
func TestTernaryEvalDispatch(t *testing.T) {
	src := "const T = 3 > 2 ? 10 : 20\nconst F = 2 > 3 ? 10 : 20\n"
	if got := evalOf(t, src, "T").Int.Int64(); got != 10 {
		t.Errorf("T = %d, want 10 (condition true, then-branch)", got)
	}
	if got := evalOf(t, src, "F").Int.Int64(); got != 20 {
		t.Errorf("F = %d, want 20 (condition false, else-branch)", got)
	}
}

// TestTernaryEvalUntakenBranchIgnored checks that only the taken branch is
// evaluated: the untaken branch may be an expression that would not fold on its
// own (a division by zero), and the ternary still folds to the taken value.
func TestTernaryEvalUntakenBranchIgnored(t *testing.T) {
	src := "const X = true ? 1 : 1 / 0\n"
	if got := evalOf(t, src, "X").Int.Int64(); got != 1 {
		t.Errorf("X = %d, want 1 (untaken else with a div-by-zero is not evaluated)", got)
	}
}

// TestTernaryEvalNested checks that a right-nested ternary folds: the else-branch
// is itself a ternary, so a clamp composes at compile time.
func TestTernaryEvalNested(t *testing.T) {
	src := "const Lo = -5 < 0 ? 0 : (-5 > 100 ? 100 : -5)\n" +
		"const Hi = 150 < 0 ? 0 : (150 > 100 ? 100 : 150)\n" +
		"const Mid = 42 < 0 ? 0 : (42 > 100 ? 100 : 42)\n"
	for _, tc := range []struct {
		name string
		want int64
	}{{"Lo", 0}, {"Hi", 100}, {"Mid", 42}} {
		if got := evalOf(t, src, tc.name).Int.Int64(); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestTernaryInMethodBody checks that a ternary works in a method body the same
// way it does in a const initializer: a non-bool condition is still reported,
// and a bool one analyzes and folds cleanly.
func TestTernaryInMethodBody(t *testing.T) {
	clean := "pub type Temp = int32 impl {\n  pub sign(): int32 {\n    return self > 0 ? 1 : (self < 0 ? -1 : 0)\n  }\n}\n"
	if _, diags := analyze(clean); len(diags) != 0 {
		t.Fatalf("clean method ternary: unexpected diagnostics: %v", codes(diags))
	}
	bad := "pub type Temp = int32 impl {\n  pub label(): int32 {\n    return self ? 1 : 0\n  }\n}\n"
	if _, diags := analyze(bad); !hasCodeSwitch(diags, CodeTernaryConditionNotBool) {
		t.Fatalf("non-bool method ternary: want ternary_condition_not_bool, got %v", codes(diags))
	}
}
