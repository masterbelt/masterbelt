// These tests pin field-type projection in type position (T.member): a declared
// field projects to its declared type with nominal identity preserved, a chain
// re-applies, and every non-field target is a distinct diagnostic rather than a
// silent fall-through. The lazy/cycle behaviour is pinned too: a grounded mutual
// projection resolves, an ungrounded one is cyclic_type_projection.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

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
	// Item is a record, but it has no field named bogus.
	src := "pub type Item = { id: long }\n" +
		"pub type T = { z: Item.bogus }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field, got %v", codes(diags))
	}
}
