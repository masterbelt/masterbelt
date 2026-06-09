// These tests pin the storage rule (§4): a type value is comptime-only and may
// not be stored, so a const, let, record field, function parameter, or result
// whose type is the metatype `type` is type_in_value_position. This is also
// where the 0001 const reification (const x = sbyte) is withdrawn — it is now an
// error, not a binding. A type value is still consumable in a comptime
// expression (asserted in analyze_projection_test.go); only storing it is
// rejected.
package semantic

import "testing"

func TestSlotConstBindingWithdrawn(t *testing.T) {
	// const x = sbyte bound a type value in 0001; under the storage rule its slot
	// type is the metatype, so it is type_in_value_position.
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
