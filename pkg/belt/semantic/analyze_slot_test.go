// These tests pin the storage rule: a type value is comptime-only and may
// not be stored, so a const, let, record field, function parameter, or result
// whose type is the metatype `type` is type_in_value_position. Binding a type
// value to a const (const x = sbyte) is therefore an error, not a binding. A
// type value is still consumable in a comptime expression (asserted in
// analyze_projection_test.go); only storing it is rejected.
package semantic

import "testing"

func TestSlotConstBindingWithdrawn(t *testing.T) {
	// Binding a type value to a const (const x = sbyte) is rejected: under the
	// storage rule its slot type is the metatype, so it is type_in_value_position.
	_, diags := analyze("pub const x = sbyte\n")
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestSlotConstMetatypeAnnotation(t *testing.T) {
	// An explicit metatype annotation is rejected the same way.
	_, diags := analyze("pub const x: type = sbyte\n")
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestSlotRecordFieldMetatype(t *testing.T) {
	// A record field whose declared type is the metatype is rejected — the field:
	// type edge of the rule (a value slot may not hold a type value).
	_, diags := analyze("pub type Schema = { kind: type }\n")
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestSlotFuncTypeMetatype(t *testing.T) {
	// A function type with a metatype parameter or result — a type-value function
	// — is rejected: there are no type-value functions (generics stay type
	// parameters).
	_, diags := analyze("pub type Remap = fn(t: type): type\n")
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestSlotFuncParamMetatype(t *testing.T) {
	// A top-level function parameter may not be a type value.
	_, diags := analyze("pub fn id(t: type): nint {\n  return 0\n}\n")
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestSlotLetMetatype(t *testing.T) {
	// A let local annotated with the metatype may not hold a type value either.
	src := "pub fn f(): nint {\n  let t: type = sbyte\n  return 0\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position, got %v", codes(diags))
	}
}

func TestLetProjectionErrorReported(t *testing.T) {
	// A failed field-type projection in a let annotation surfaces its diagnostic,
	// the same as in a type or const annotation — not a silent Invalid.
	src := "pub type Item = { id: long }\n" +
		"pub fn f(): nint {\n  let x: Item.nope = 1\n  return x\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field in let annotation, got %v", codes(diags))
	}
}

func TestFuncSignatureProjectionErrorReported(t *testing.T) {
	// A failed field-type projection in a function parameter surfaces its
	// diagnostic, the same as in a type or const annotation.
	src := "pub type Item = { id: long }\n" +
		"pub fn f(x: Item.nope): nint {\n  return 0\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field in function signature, got %v", codes(diags))
	}
}

func TestSlotEnumMethodMetatype(t *testing.T) {
	// An enum method's parameter may not be a type value — enum methods are
	// resolved in their own loop, which must obey the storage rule too.
	src := "pub enum E {\n  A\n} impl {\n  pub f(x: type): nint {\n    return 0\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position on enum method, got %v", codes(diags))
	}
}

func TestSlotInterfaceMethodMetatype(t *testing.T) {
	// An interface member's parameter may not be a type value — interfaces are
	// resolved in their own loop, which must obey the storage rule, so an
	// interface cannot expose a type-valued runtime slot.
	src := "pub interface I {\n  f(x: type): nint\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position on interface member, got %v", codes(diags))
	}
}

func TestSlotAssocConstMetatypeAnnotated(t *testing.T) {
	// An associated constant annotated with the metatype may not store a type
	// value, the impl-block twin of the top-level const rule.
	src := "pub type T = nint impl {\n  pub const C: type = sbyte\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position on annotated assoc const, got %v", codes(diags))
	}
}

func TestSlotAssocConstMetatypeInferred(t *testing.T) {
	// An associated constant inferred from a type-value initializer is rejected
	// too — the check runs after the fold settles its type.
	src := "pub type T = nint impl {\n  pub const C = sbyte\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position on inferred assoc const, got %v", codes(diags))
	}
}

func TestSlotMasterRowFieldMetatype(t *testing.T) {
	// A master row column may not store a type value — the row is the master's
	// record body, so the record-field storage rule applies to it too.
	src := "master M {\n  record {\n    id: int,\n    x: type,\n  }\n  primary id\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeInValuePosition) {
		t.Fatalf("want type_in_value_position on master row field, got %v", codes(diags))
	}
}

func TestSlotLambdaMetatypeRejected(t *testing.T) {
	// A function literal written inline (not stored) may not be a type-value
	// function either: a type-value parameter or result is type_in_value_position,
	// the same rule a declared signature obeys.
	_, dp := analyze("assert (fn(x: type): bool { return true })(long)\n")
	if !hasCode(dp, CodeTypeInValuePosition) {
		t.Fatalf("lambda param: want type_in_value_position, got %v", codes(dp))
	}
	_, dr := analyze("assert (fn(): type { return long })() == long\n")
	if !hasCode(dr, CodeTypeInValuePosition) {
		t.Fatalf("lambda result: want type_in_value_position, got %v", codes(dr))
	}
}

func TestSlotProjectionAnnotationAllowed(t *testing.T) {
	// A projected type is not the metatype — it is the field's declared type — so
	// a const annotated with one is fine: x has type long.
	src := "pub type Item = { id: long }\n" +
		"pub const x: Item.id = 1\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (projection is a real type), got %v", codes(diags))
	}
}
