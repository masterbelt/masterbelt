// This file tests type-position member projection (Character.level): a type
// expression Type.member resolving to the member's declared type across the
// member kinds, the grounded cycles it tolerates, and the ungrounded cycle and
// unknown member it rejects.
package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// fieldType returns the resolved type of a record-bodied type's field by index,
// failing the test if the type is missing or not a record.
func fieldType(t *testing.T, m *ir.Module, typeName string, field int) ir.Type {
	t.Helper()
	for _, d := range m.Types {
		if d.Name == typeName {
			rec, ok := d.Body.(*ir.Record)
			if !ok {
				t.Fatalf("%s body = %T, want Record", typeName, d.Body)
			}
			return rec.Fields[field].Type
		}
	}
	t.Fatalf("type %s not found", typeName)
	return nil
}

func TestTypeProjectionField(t *testing.T) {
	// Monster.level projects Character.level to the declared alias Level — not
	// the sbyte it aliases. Type values carry declared identity, so a later
	// schema change to Level follows.
	m, diags := analyze("pub type Level = sbyte\npub type Character = { level: Level }\npub type Monster = { level: Character.level }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	got := fieldType(t, m, "Monster", 0)
	named, ok := got.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Name != "Level" {
		t.Fatalf("Monster.level = %s (%T), want Named -> Level", got, got)
	}
}

func TestTypeProjectionAssocConst(t *testing.T) {
	// A type-position projection of an associated constant resolves to the
	// constant's declared type, the alias preserved: Stat.Top is a Cap.
	m, diags := analyze("pub type Cap = sbyte\npub type Stat = sbyte impl {\n  pub const Top: Cap = 99\n}\npub type X = { hi: Stat.Top }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	got := fieldType(t, m, "X", 0)
	named, ok := got.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Name != "Cap" {
		t.Errorf("X.hi = %s (%T), want Named -> Cap", got, got)
	}
}

func TestTypeProjectionEnumMember(t *testing.T) {
	// A type-position projection of an enum member resolves to the enum type
	// itself — the member is a value of the enum.
	m, diags := analyze("pub enum Color { Red, Green }\npub type Pick = { c: Color.Red }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	got := fieldType(t, m, "Pick", 0)
	named, ok := got.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Name != "Color" {
		t.Fatalf("Pick.c = %s (%T), want Named -> Color", got, got)
	}
}

func TestTypeProjectionGroundedCycle(t *testing.T) {
	// Mutual references resolve when a projection bottoms out on a concrete
	// type: B.x grounds on sbyte, so A.y that projects it resolves, even though
	// A and B reference each other by name.
	m, diags := analyze("pub type A = { b: B, y: B.x }\npub type B = { a: A, x: sbyte }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := fieldType(t, m, "A", 1); got.String() != "sbyte" {
		t.Errorf("A.y = %s, want sbyte", got)
	}
}

func TestTypeProjectionCyclic(t *testing.T) {
	// A.x projects B.x which projects A.x: no ground, no fixed point.
	_, diags := analyze("pub type A = { x: B.x }\npub type B = { x: A.x }\n")
	if !hasCode(diags, CodeCyclicTypeProjection) {
		t.Fatalf("codes = %v, want cyclic_type_projection", codes(diags))
	}
}

func TestTypeProjectionUnknownMember(t *testing.T) {
	// A projection of a member the receiver does not declare is an unknown type.
	_, diags := analyze("pub type Character = { level: sbyte }\npub type Bad = { x: Character.bogus }\n")
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("codes = %v, want unknown_type", codes(diags))
	}
}

func TestTypeProjectionAliasChain(t *testing.T) {
	// recordOf follows an alias chain to the underlying record: C.y projects
	// AliasB.x through type AliasB = AliasA = Rec, the way the value-position
	// field read resolves a chained record alias.
	m, diags := analyze("pub type Rec = { x: sbyte }\npub type AliasA = Rec\npub type AliasB = AliasA\npub type C = { y: AliasB.x }\n")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := fieldType(t, m, "C", 0); got.String() != "sbyte" {
		t.Errorf("C.y = %s, want sbyte", got)
	}
}

func TestTypeProjectionGenericReceiverRejected(t *testing.T) {
	// A member of an unapplied generic receiver is not projectable: projecting
	// Box.value would leak Box's free type parameter into Use. It reads as
	// unknown_type and the field type is Invalid, not a leaked TypeVar.
	m, diags := analyze("pub type Box<T> = { value: T }\npub type Use = { v: Box.value }\n")
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("codes = %v, want unknown_type", codes(diags))
	}
	if got := fieldType(t, m, "Use", 0); got != ir.Invalid {
		t.Errorf("Use.v = %s (%T), want Invalid (no TypeVar leak)", got, got)
	}
}

func TestTypeProjectionOverloadedStaticRejected(t *testing.T) {
	// An overloaded static fn cannot be projected: a type position carries no
	// call arguments to select an overload, so the result would be order-dependent.
	src := "pub type C = { d: nint } impl {\n" +
		"  pub static fn make(v: nint): C { return C{ d: v } }\n" +
		"  pub static fn make(c: C): C { return c }\n" +
		"}\n" +
		"pub type Use = { m: C.make }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeAmbiguousStaticProjection) {
		t.Fatalf("codes = %v, want ambiguous_static_projection", codes(diags))
	}
}

func TestTypeProjectionSingleStatic(t *testing.T) {
	// A single (non-overloaded) static fn projects to its function type.
	src := "pub type C = { d: nint } impl {\n" +
		"  pub static fn zero(): C { return C{ d: 0 } }\n" +
		"}\n" +
		"pub type Use = { m: C.zero }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := fieldType(t, m, "Use", 0); !isFunc(got) {
		t.Errorf("Use.m = %s (%T), want Func", got, got)
	}
}

func isFunc(t ir.Type) bool {
	_, ok := t.(*ir.Func)
	return ok
}

func TestTypeProjectionNotInMethodBody(t *testing.T) {
	// A projection annotation inside a concrete method body is not covered by the
	// declaration fold pass, so it must not be emitted there: the body keeps the
	// prior meaning of a qualified name (reported as unknown_type, like a
	// top-level function body), and no transient Projection node leaks into the
	// serialized IR.
	src := "pub type Level = sbyte\n" +
		"pub type Character = { level: Level }\n" +
		"pub type Foo = sbyte impl {\n" +
		"  pub bar(): sbyte {\n" +
		"    let x: Character.level = 1\n" +
		"    return x\n" +
		"  }\n" +
		"}\n"
	m, _ := analyze(src)
	txt, err := m.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if strings.Contains(string(txt), "Projection") {
		t.Errorf("a Projection node leaked into method-body IR:\n%s", txt)
	}
}
