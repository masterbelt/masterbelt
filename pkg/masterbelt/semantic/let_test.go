package semantic

import (
	"testing"
)

// TestLetInferredOK checks that an inferred let and a reassignment of the same
// type analyze cleanly: a mutable local is a legal body construct.
func TestLetInferredOK(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  let x = n\n  x = x + 1\n  return x\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestLetAnnotatedOK checks that an annotated let and an assignment that stays
// assignable to the annotation analyze cleanly.
func TestLetAnnotatedOK(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  let x: int = n\n  x = 5\n  return x\n}\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

// TestAssignToParam checks that assigning to a parameter is reported: a
// parameter is immutable, so the target is not a let local (assign_to_const).
func TestAssignToParam(t *testing.T) {
	_, diags := analyze("pub fn f(n: int): int {\n  n = 5\n  return n\n}\n")
	if !hasCodeSwitch(diags, CodeAssignToConst) {
		t.Fatalf("want assign_to_const for a parameter target, got %v", codes(diags))
	}
}

// TestAssignToConst checks that assigning to a top-level constant is reported as
// assign_to_const: a const is immutable.
func TestAssignToConst(t *testing.T) {
	_, diags := analyze("const C = 1\npub fn f(): int {\n  C = 2\n  return C\n}\n")
	if !hasCodeSwitch(diags, CodeAssignToConst) {
		t.Fatalf("want assign_to_const for a const target, got %v", codes(diags))
	}
}

// TestAssignToUndefined checks that assigning to a name with no binding in scope
// is reported as assign_to_undefined.
func TestAssignToUndefined(t *testing.T) {
	_, diags := analyze("pub fn f(): int {\n  x = 5\n  return 0\n}\n")
	if !hasCodeSwitch(diags, CodeAssignToUndefined) {
		t.Fatalf("want assign_to_undefined for an unbound target, got %v", codes(diags))
	}
}

// TestAssignTypeMismatch checks that reassigning a let to a value of a different
// type is reported as assign_type_mismatch: the let's type is fixed.
func TestAssignTypeMismatch(t *testing.T) {
	_, diags := analyze("pub fn f(): int {\n  let n = 0\n  n = \"s\"\n  return n\n}\n")
	if !hasCodeSwitch(diags, CodeAssignTypeMismatch) {
		t.Fatalf("want assign_type_mismatch for a type-changing assignment, got %v", codes(diags))
	}
}

// TestImmutableData checks that assigning to a field of a value is reported as
// immutable_data: record and collection data are immutable, so the target is not
// a let local.
func TestImmutableData(t *testing.T) {
	src := "pub type P = { x: int }\npub fn f(p: P): int {\n  p.x = 5\n  return p.x\n}\n"
	_, diags := analyze(src)
	if !hasCodeSwitch(diags, CodeImmutableData) {
		t.Fatalf("want immutable_data for a field target, got %v", codes(diags))
	}
}

// TestMissingInitializer checks that a let with no initializer is reported as
// missing_initializer: a let must be initialized in place.
func TestMissingInitializer(t *testing.T) {
	_, diags := analyze("pub fn f(): int {\n  let m: int\n  return 0\n}\n")
	if !hasCodeSwitch(diags, CodeMissingInitializer) {
		t.Fatalf("want missing_initializer for an uninitialized let, got %v", codes(diags))
	}
}

// TestLetEvalFolds checks that a function whose body uses a let and a guarded
// reassignment still folds at compile time: a local mutation is not an effect,
// so the function stays pure and the value collapses.
func TestLetEvalFolds(t *testing.T) {
	src := "pub fn pick(flag: bool): int {\n  let base = 10\n  let result = base\n  if flag {\n    result = base * 2\n  }\n  return result\n}\nconst A = pick(true)\nconst B = pick(false)\n"
	if got := evalOf(t, src, "A").Int.Int64(); got != 20 {
		t.Errorf("A = %d, want 20 (guarded reassignment taken)", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 10 {
		t.Errorf("B = %d, want 10 (guard not taken)", got)
	}
}

// TestLetShadowFolds checks that an inner block's let is a fresh binding that
// shadows the outer one: the inner reassignment updates the inner slot, and the
// outer binding is untouched after the block.
func TestLetShadowFolds(t *testing.T) {
	src := "pub fn shadow(flag: bool): int {\n  let result = 1\n  if flag {\n    let result = 2\n    result = result + 10\n    return result\n  }\n  return result\n}\nconst A = shadow(true)\nconst B = shadow(false)\n"
	if got := evalOf(t, src, "A").Int.Int64(); got != 12 {
		t.Errorf("A = %d, want 12 (inner shadow reassigned)", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 1 {
		t.Errorf("B = %d, want 1 (outer binding untouched)", got)
	}
}

// TestAssignOuterFromBlockFolds checks that a reassignment inside a block updates
// an outer let (no inner shadow), and the new value is visible after the block.
func TestAssignOuterFromBlockFolds(t *testing.T) {
	src := "pub fn pick(flag: bool): int {\n  let result = 1\n  if flag {\n    result = 9\n  }\n  return result\n}\nconst A = pick(true)\nconst B = pick(false)\n"
	if got := evalOf(t, src, "A").Int.Int64(); got != 9 {
		t.Errorf("A = %d, want 9 (outer reassigned in block)", got)
	}
	if got := evalOf(t, src, "B").Int.Int64(); got != 1 {
		t.Errorf("B = %d, want 1 (guard not taken)", got)
	}
}

// TestLetStaysPure checks that a function using a let keeps no effect: a local
// mutation is internal computation, not an effect, so the function is pure and a
// const may call it.
func TestLetStaysPure(t *testing.T) {
	_, diags := analyze("pub fn step(n: int): int {\n  let acc = n\n  acc = acc + 1\n  return acc\n}\nconst A = step(41)\n")
	if len(diags) != 0 {
		t.Fatalf("a let-using function should stay pure and foldable, got %v", codes(diags))
	}
}
