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

func TestTypeProjectionSignatureDeferred(t *testing.T) {
	// Signature-position projection is deferred to a later track: a static fn (a
	// signature), a method signature, and a top-level function signature keep the
	// prior meaning of a qualified name (an unknown type), rather than projecting.
	base := "pub type Level = sbyte\npub type Character = { level: Level }\n"
	cases := []string{
		// a static fn projected as a value type
		base + "pub type C = sbyte impl {\n  pub static fn zero(): C { return 0 }\n}\npub type Use = { m: C.zero }\n",
		// a method signature parameter
		base + "pub type C = sbyte impl {\n  pub m(x: Character.level): sbyte { return 0 }\n}\n",
		// a top-level function signature result
		base + "pub fn f(): Character.level { return 0 }\n",
	}
	for i, src := range cases {
		_, diags := analyze(src)
		if !hasCode(diags, CodeUnknownType) {
			t.Errorf("case %d: signature projection should be deferred (unknown_type); got %v", i, codes(diags))
		}
	}
}

func TestTypeProjectionGenericArgBound(t *testing.T) {
	// A projected type used as a bounded generic argument is checked against the
	// bound after it folds, not as the unfolded projection: Character.level folds
	// to Level (an int, comparable), so pair<Character.level> is clean.
	src := "pub type Level = int\n" +
		"pub type Character = { level: Level }\n" +
		"pub type pair<T: comparable> = list<T>\n" +
		"pub type Use = pair<Character.level>\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

func TestTypeProjectionGenericArgBoundViolation(t *testing.T) {
	// The deferred bound check still fires: a projected type that does not satisfy
	// the bound (a record is not comparable) is reported, not silently accepted.
	src := "pub type Rec = { a: nint }\n" +
		"pub type Holder = { r: Rec }\n" +
		"pub type pair<T: comparable> = list<T>\n" +
		"pub type Use = pair<Holder.r>\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("codes = %v, want bound_not_satisfied", codes(diags))
	}
}

func TestTypeProjectionImplTag(t *testing.T) {
	// An impl tag that is a projection folds to the interface before it is
	// classified: impl Holder.g opts X into Greet (X supplies hello), rather than
	// being reported as not-an-interface.
	src := "pub interface Greet {\n  hello(): nint\n}\n" +
		"pub type Holder = { g: Greet }\n" +
		"pub type X = nint impl Holder.g {\n  hello(): nint { return 1 }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeNotAnInterface) {
		t.Fatalf("impl Holder.g reported not_an_interface; codes = %v", codes(diags))
	}
}

func TestTypeProjectionInRefinement(t *testing.T) {
	// A refined alias whose body is a projection: the refinement is checked after
	// the body folds, so Holder.base folds to int and the predicate self > 0 is
	// valid rather than an operation on an unresolved projection.
	src := "pub type Holder = { base: int }\n" +
		"pub type Pos = Holder.base where self > 0\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

func TestTypeProjectionMasterRow(t *testing.T) {
	// A master row written through a projection: the row folds to the record
	// before the key check, so Thing keys against the concrete row rather than
	// being reported as a missing row.
	src := "pub type Row = { id: int, name: string }\n" +
		"pub type Holder = { row: Row }\n" +
		"pub master Thing {\n  record Holder.row\n  primary id\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

func TestTypeProjectionInterfaceParent(t *testing.T) {
	// An interface parent that is a projection folds to the interface before the
	// inheritance check, so Child inherits Greet rather than being reported as
	// extending a non-interface.
	src := "pub interface Greet {\n  hello(): nint\n}\n" +
		"pub type Holder = { g: Greet }\n" +
		"pub interface Child: Holder.g {\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeNotAnInterface) {
		t.Fatalf("interface Child: Holder.g reported not_an_interface; codes = %v", codes(diags))
	}
}

func TestTypeProjectionGenericParamBound(t *testing.T) {
	// A generic parameter's bound written as a projection is judged after it
	// folds: Holder.s folds to Show, and Level (which opts into Show) satisfies it,
	// so Need<Level> is clean — the bound projection does not falsely reject it.
	src := "pub interface Show {\n  show(): nint\n}\n" +
		"pub type Level = int impl Show {\n  show(): nint { return 0 }\n}\n" +
		"pub type Holder = { s: Show }\n" +
		"pub type Need<T: Holder.s> = list<T>\n" +
		"pub type Use = Need<Level>\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

func TestTypeProjectionEnumBaseDeferred(t *testing.T) {
	// An enum base written as a projection is not folded (the enum needs its
	// concrete base in place to fold the member values), so it reads as an unknown
	// type rather than an invalid-base report on an unresolved projection.
	src := "pub type Holder = { b: byte }\n" +
		"pub enum E: Holder.b {\n  A = 1\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownType) {
		t.Fatalf("enum base projection should be deferred (unknown_type); got %v", codes(diags))
	}
}

func TestTypeProjectionFieldNotMaskedByStatic(t *testing.T) {
	// A record field projects even when a static fn shares its name (different
	// namespaces): C.x is the field's declared type, not masked by the deferred
	// static x.
	src := "pub type C = { x: nint } impl {\n  pub static fn x(): nint { return 0 }\n}\n" +
		"pub type Use = { v: C.x }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	if got := fieldType(t, m, "Use", 0); got.String() != "nint" {
		t.Errorf("Use.v = %s, want nint", got)
	}
}

func TestTypeProjectionMemberCollisionOnProjectedBody(t *testing.T) {
	// A body written through a projection is checked for member collisions after it
	// folds: type C = Holder.row, where Holder.row is a record with field x, plus
	// an accessor x() — the collision is reported because checkMemberDecls reads
	// the folded record's fields.
	src := "pub type Holder = { row: { x: nint } }\n" +
		"pub type C = Holder.row impl {\n  pub get x(): nint { return 0 }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeAccessorCollision) {
		t.Fatalf("accessor x collides with the projected body's field x; want accessor_collision, got %v", codes(diags))
	}
}

func TestTypeProjectionUnannotatedConst(t *testing.T) {
	// A projected associated constant must be annotated: its type is inferred from
	// its value in a later pass, so projecting an unannotated one is reported
	// rather than publishing a malformed nil field type.
	src := "pub type Stat = sbyte impl {\n  pub const Top = 99\n}\n" +
		"pub type X = { hi: Stat.Top }\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeUnannotatedConstProjection) {
		t.Fatalf("codes = %v, want unannotated_const_projection", codes(diags))
	}
	if got := fieldType(t, m, "X", 0); got != ir.Invalid {
		t.Errorf("X.hi = %v (%T), want Invalid (not a malformed nil type)", got, got)
	}
}

func TestTypeProjectionGenericArgUserBound(t *testing.T) {
	// A projected generic argument whose bound is a user interface is judged after
	// the impls resolve, not during the fold: Character.level folds to Level,
	// which opts into Show, so pair<Character.level> satisfies pair<T: Show>.
	src := "pub interface Show {\n  show(): nint\n}\n" +
		"pub type Level = int impl Show {\n  show(): nint { return 0 }\n}\n" +
		"pub type Character = { level: Level }\n" +
		"pub type pair<T: Show> = list<T>\n" +
		"pub type Use = pair<Character.level>\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
}

func TestTypeProjectionGenericArgUserBoundViolation(t *testing.T) {
	// The deferred user-bound check genuinely fires: Level does not opt into Show,
	// so pair<Character.level> is reported (it is not silently accepted).
	src := "pub interface Show {\n  show(): nint\n}\n" +
		"pub type Level = int\n" +
		"pub type Character = { level: Level }\n" +
		"pub type pair<T: Show> = list<T>\n" +
		"pub type Use = pair<Character.level>\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("Level does not impl Show; want bound_not_satisfied, got %v", codes(diags))
	}
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
