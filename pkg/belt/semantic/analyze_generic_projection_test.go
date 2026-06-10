// These tests pin generic field-type projection: projecting a field off a
// generic type instantiates it, substituting the application's arguments for the
// definition's parameters through the field's declared type. The four forms — a
// direct generic application, a concrete alias of one, an applied generic-alias
// chain, and a forward-referenced generic — all resolve; a bare generic with no
// application (no arguments to substitute) stays generic_type_projection.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestGenericProjectionDirectTypePosition(t *testing.T) {
	// Box<string>.value (written Box.value<string>) substitutes string for T
	// through the field type T, yielding string.
	src := "pub type Box<T> = { value: T }\n" +
		"pub type S = { v: Box.value<string> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "S", "v"); got == nil || got.String() != "string" {
		t.Fatalf("S.v = %v, want string", got)
	}
}

func TestGenericProjectionMultipleParams(t *testing.T) {
	// A two-parameter generic substitutes each argument positionally: Pair<int,
	// string>.first is int and .second is string, so the arguments are not
	// transposed or dropped.
	src := "pub type Pair<A, B> = { first: A, second: B }\n" +
		"pub type S = { a: Pair.first<int, string>, b: Pair.second<int, string> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "S", "a"); got == nil || got.String() != "int" {
		t.Fatalf("S.a = %v, want int", got)
	}
	if got := fieldType(m, "S", "b"); got == nil || got.String() != "string" {
		t.Fatalf("S.b = %v, want string", got)
	}
}

func TestGenericProjectionPreservesNominalIdentity(t *testing.T) {
	// Substitution keeps the argument's nominal identity: Box<Level>.value is the
	// declared alias Level (a Named), not the sbyte it unwraps to — the same
	// nominal rule a non-generic projection follows.
	src := "pub type Level = sbyte\n" +
		"pub type Box<T> = { value: T }\n" +
		"pub type S = { v: Box.value<Level> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	got := fieldType(m, "S", "v")
	named, ok := got.(*ir.Named)
	if !ok || named.Def == nil || named.Def.Name != "Level" {
		t.Fatalf("S.v = %v, want Level (a Named)", got)
	}
}

func TestGenericProjectionAliasChainTypePosition(t *testing.T) {
	// An applied generic-alias chain composes substitutions: Box<T> = Inner<T>, so
	// Box.value<string> instantiates Box<string>, then Inner<string>, yielding the
	// field type string through both steps.
	src := "pub type Inner<T> = { value: T }\n" +
		"pub type Box<T> = Inner<T>\n" +
		"pub type S = { v: Box.value<string> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "S", "v"); got == nil || got.String() != "string" {
		t.Fatalf("S.v = %v, want string (through the alias chain)", got)
	}
}

func TestGenericProjectionConcreteAliasTypePosition(t *testing.T) {
	// A concrete alias of a generic application (StringBox = Box<string>) reaches
	// the projector as a Named, not an App, so projecting its field in type
	// position must still instantiate through the alias body's application — and
	// agree with the value position — rather than reporting type_has_no_fields.
	src := "pub type Box<T> = { value: T }\n" +
		"pub type StringBox = Box<string>\n" +
		"pub type Aliased = StringBox.value\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "Aliased" {
			if def.Body == nil || def.Body.String() != "string" {
				t.Fatalf("Aliased = %v, want string (StringBox.value through the alias)", def.Body)
			}
		}
	}
}

func TestGenericProjectionForwardReference(t *testing.T) {
	// The projecting type precedes the generic it projects (a forward reference):
	// the field type is resolved from the generic's declaration syntax in its own
	// parameter scope, then the application's argument is substituted — so
	// Box.value<string> is still string when Box is defined after S.
	src := "pub type S = { v: Box.value<string> }\n" +
		"pub type Box<T> = { value: T }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	if got := fieldType(m, "S", "v"); got == nil || got.String() != "string" {
		t.Fatalf("S.v = %v, want string (forward-referenced generic)", got)
	}
}

func TestGenericProjectionValueConsumedByAssert(t *testing.T) {
	// A generic projection through a concrete alias is a comptime type value an
	// assert consumes: Box = Inner<Level>, so Box.value is Level (nominal), equal
	// to Level and not the sbyte it aliases.
	src := "pub type Level = sbyte\n" +
		"pub type Inner<T> = { value: T }\n" +
		"pub type Box = Inner<Level>\n" +
		"assert Box.value == Level\n" +
		"assert Box.value != sbyte\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
}

func TestGenericProjectionBareGenericStillRejected(t *testing.T) {
	// A bare generic type with no application has no arguments to substitute, so a
	// type-position projection off it (type S = Box.value, no <...>) stays
	// generic_type_projection rather than leaking the unbound parameter T.
	src := "pub type Box<T> = { value: T }\n" +
		"pub type S = Box.value\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeGenericTypeProjection) {
		t.Fatalf("want generic_type_projection, got %v", codes(diags))
	}
}

func TestGenericProjectionUnknownFieldInstantiated(t *testing.T) {
	// An instantiated generic still validates the field name: Box<string>.nope
	// names no field of the record, so it is unknown_field — the generic path does
	// not swallow a typo into an unbound result.
	src := "pub type Box<T> = { value: T }\n" +
		"pub type S = { v: Box.nope<string> }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeUnknownField) {
		t.Fatalf("want unknown_field, got %v", codes(diags))
	}
}
