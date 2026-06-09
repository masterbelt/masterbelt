// These tests pin field-type projection in type position (T.member): a declared
// field projects to its declared type with nominal identity preserved, a chain
// re-applies, and every non-field target is a distinct diagnostic rather than a
// silent fall-through. The lazy/cycle behaviour is pinned too: a grounded mutual
// projection resolves, an ungrounded one is cyclic_type_projection.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// diagWith returns the first diagnostic of the given code, or a zero value.
func diagWith(diags []diagnostic.Diagnostic, code diagnostic.Code) diagnostic.Diagnostic {
	for _, d := range diags {
		if d.Code == code {
			return d
		}
	}
	return diagnostic.Diagnostic{}
}

// fieldType returns the resolved type of field in the named record-aliased type
// of the module, or nil when the type or field is absent.
func fieldType(m *ir.Module, typeName, field string) ir.Type {
	for _, def := range m.Types {
		if def.Name != typeName {
			continue
		}
		rec, ok := def.Body.(*ir.Record)
		if !ok {
			return nil
		}
		for _, f := range rec.Fields {
			if f.Name == field {
				return f.Type
			}
		}
	}
	return nil
}

func TestTypeProjectionPreservesDeclaredType(t *testing.T) {
	src := "pub type Level = sbyte\n" +
		"pub type Item = { id: long, level: Level }\n" +
		"pub type Mob = { rank: Item.level }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	// Mob.rank projects Item.level, which is the declared alias Level — not the
	// sbyte it unwraps to.
	got := fieldType(m, "Mob", "rank")
	named, ok := got.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Name != "Level" {
		t.Fatalf("Mob.rank = %v, want Level (a Named)", got)
	}
}

func TestTypeProjectionChainAndForwardReference(t *testing.T) {
	// Mob precedes Item in source order (a forward reference), and the chain
	// Order.customer.id re-applies the projection through Customer to its id.
	src := "pub type Mob = { rank: Item.level }\n" +
		"pub type Level = sbyte\n" +
		"pub type Item = { id: long, level: Level }\n" +
		"pub type Customer = { id: long }\n" +
		"pub type Order = { customer: Customer }\n" +
		"pub type Ref = { who: Order.customer, cid: Order.customer.id }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "Mob", "rank"); got == nil || got.String() != "Level" {
		t.Fatalf("Mob.rank = %v, want Level", got)
	}
	if got := fieldType(m, "Ref", "who"); got == nil || got.String() != "Customer" {
		t.Fatalf("Ref.who = %v, want Customer", got)
	}
	if got := fieldType(m, "Ref", "cid"); got == nil || got.String() != "long" {
		t.Fatalf("Ref.cid = %v, want long", got)
	}
}

func TestTypeProjectionGroundedCycleResolves(t *testing.T) {
	// A mutual projection with a concrete floor: Hero.partner projects Villain,
	// Villain.partner projects Hero — each carries the other's declared record
	// type, which is grounded, so it resolves with no cyclic diagnostic.
	src := "pub type Hero = { name: long, partner: Villain }\n" +
		"pub type Villain = { name: long, partner: Hero }\n" +
		"pub type Link = { h: Hero.partner, v: Villain.partner }\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (grounded cycle), got %v", codes(diags))
	}
}

func TestTypeProjectionUngroundedCycleRejected(t *testing.T) {
	// A.x projects B.x projects A.x with no concrete type in between: there is no
	// floor, so it is cyclic_type_projection.
	src := "pub type A = { x: B.x }\n" +
		"pub type B = { x: A.x }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeCyclicTypeProjection) {
		t.Fatalf("want cyclic_type_projection, got %v", codes(diags))
	}
}

func TestTypeProjectionMemberIsNotAType(t *testing.T) {
	// sbyte.Max is an associated constant — a value, not a type.
	src := "pub type T = { z: sbyte.Max }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMemberIsNotAType) {
		t.Fatalf("want member_is_not_a_type, got %v", codes(diags))
	}
}

func TestTypeProjectionTypeHasNoFields(t *testing.T) {
	// sbyte is a primitive: it has no fields to project from.
	src := "pub type T = { z: sbyte.whatever }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeHasNoFields) {
		t.Fatalf("want type_has_no_fields, got %v", codes(diags))
	}
}

func TestTypeProjectionUnknownField(t *testing.T) {
	// Item is a record, but it has no field named bogus. The diagnostic reads
	// "Item has no field bogus": the receiver type fills {typ}, the missing field
	// fills {field} — not the reverse.
	src := "pub type Item = { id: long }\n" +
		"pub type T = { z: Item.bogus }\n"
	_, diags := analyze(src)
	d := diagWith(diags, CodeUnknownField)
	if d.Code != CodeUnknownField {
		t.Fatalf("want unknown_field, got %v", codes(diags))
	}
	if got := d.Fields["typ"].String(); got != "Item" {
		t.Errorf("unknown_field typ = %q, want Item", got)
	}
	if got := d.Fields["field"].String(); got != "bogus" {
		t.Errorf("unknown_field field = %q, want bogus", got)
	}
}

func TestTypeProjectionGenericInstantiation(t *testing.T) {
	// A generic head instantiates the projected field type: Box<string>.value is
	// string, not the unbound parameter T the field declares — the arguments are
	// applied, not dropped.
	src := "pub type Box<T> = { value: T }\n" +
		"pub type S = { v: Box.value<string> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "S", "v"); got == nil || got.String() != "string" {
		t.Fatalf("S.v = %v, want string (Box<string>.value), not the unbound T", got)
	}
}

func TestTypeProjectionKeywordFieldName(t *testing.T) {
	// A column named with a keyword (type) projects in type position exactly as in
	// value position: the keyword is read as a member name after the dot.
	src := "pub type Level = sbyte\n" +
		"pub type Schema = { type: Level }\n" +
		"pub type X = Schema.type\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "X" {
			if def.Body == nil || def.Body.String() != "Level" {
				t.Fatalf("X = %v, want Level (Schema.type projection)", def.Body)
			}
		}
	}
}

func TestTypeProjectionMatchArmError(t *testing.T) {
	// A failed field-type projection in a match arm type surfaces its diagnostic
	// rather than being silently dropped.
	src := "pub type Item = { id: long }\n" +
		"pub fn f(v: Item | error): nint {\n  match v {\n    Item.nope x -> return 1\n    _ -> return 0\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field in match arm, got %v", codes(diags))
	}
}

func TestTypeProjectionValueConsumedByAssert(t *testing.T) {
	// A type value projected in value position (Item.id) is comptime-only; an
	// assert may consume it through == (type equality), which folds to a bool —
	// nominal identity, so Item.level is Level (true) and not sbyte (false).
	src := "pub type Level = sbyte\n" +
		"pub type Item = { id: long, level: Level }\n" +
		"assert Item.id == long\n" +
		"assert Item.level == Level\n" +
		"assert Item.id != sbyte\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
}

func TestTypeProjectionAssertNominalIdentity(t *testing.T) {
	// Item.level is the declared alias Level, not the sbyte it unwraps to: the
	// equality is by nominal identity, so == sbyte is false and the assert fails.
	src := "pub type Level = sbyte\n" +
		"pub type Item = { level: Level }\n" +
		"assert Item.level == sbyte\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeAssertionFailed) {
		t.Fatalf("want assertion_failed (Level != sbyte), got %v", codes(diags))
	}
}
