// These tests pin getter type-projection: a getter is a readable member, so
// projecting it off a type (Item.level where level is a getter) yields the
// getter's result type — symmetric with a field projection (Item.id) and with
// the value-position read (item.level). A self-returning getter projects the
// receiver type; a method is still not a type.
package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func TestGetterProjectionTypePosition(t *testing.T) {
	// level is a getter; projecting it in type position yields its result type,
	// exactly as a field projection does.
	src := "pub type Item = { n: nint } impl {\n  pub get level(): long { return self.n }\n}\n" +
		"pub type L = Item.level\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "L" {
			if def.Body == nil || def.Body.String() != "long" {
				t.Fatalf("L = %v, want long (Item.level getter projection)", def.Body)
			}
		}
	}
}

func TestGetterProjectionValuePosition(t *testing.T) {
	// A getter projected in value position is a comptime type value an assert
	// consumes — the read/projection symmetry, the value half.
	src := "pub type Item = { n: nint } impl {\n  pub get level(): long { return self.n }\n}\n" +
		"assert Item.level == long\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (Item.level == long folds), got %v", codes(diags))
	}
}

func TestGetterProjectionSelfResult(t *testing.T) {
	// A getter returning self projects the receiver type, as a self-returning
	// method/getter read does in value position.
	src := "pub type Item = { n: nint } impl {\n  pub get same(): self { return self }\n}\n" +
		"pub type S = Item.same\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "S" {
			named, ok := def.Body.(*ir.Named)
			if !ok || named.Def == nil || named.Def.Name != "Item" {
				t.Fatalf("S = %v, want Item (Item.same self getter)", def.Body)
			}
		}
	}
}

func TestFieldProjectionStillWorks(t *testing.T) {
	// A field projection is unchanged: a field wins (the two never share a name),
	// and the getter path does not disturb it.
	src := "pub type Item = { id: long } impl {\n  pub get level(): nint { return 1 }\n}\n" +
		"pub type A = Item.id\n" +
		"pub type B = Item.level\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean, got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "A" && (def.Body == nil || def.Body.String() != "long") {
			t.Fatalf("A = %v, want long (field)", def.Body)
		}
		if def.Name == "B" && (def.Body == nil || def.Body.String() != "nint") {
			t.Fatalf("B = %v, want nint (getter)", def.Body)
		}
	}
}

func TestMethodStillNotAType(t *testing.T) {
	// A (non-getter) method is not a readable member: projecting it is still
	// member_is_not_a_type, not a getter projection.
	src := "pub type Item = { n: nint } impl {\n  pub fn calc(): nint { return self.n }\n}\n" +
		"pub type X = Item.calc\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMemberIsNotAType) {
		t.Fatalf("want member_is_not_a_type, got %v", codes(diags))
	}
}

func TestGetterProjectionBareGenericRejected(t *testing.T) {
	// A getter on a bare generic has no application to instantiate, so projecting
	// it in value position is rejected just as a field is — not reified as the free
	// parameter T.
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"assert Box.item == string\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeAssertionFailed) || len(diags) == 0 {
		t.Fatalf("want the projection rejected (not a free-T reification), got %v", codes(diags))
	}
}

func TestGetterProjectionForwardReference(t *testing.T) {
	// A getter on a type declared after the projecting alias resolves through the
	// declaration syntax, exactly as a forward field projection does.
	src := "pub type L = Item.level\n" +
		"pub type Item = { n: nint } impl {\n  pub get level(): long { return self.n }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward getter projection), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "L" && (def.Body == nil || def.Body.String() != "long") {
			t.Fatalf("L = %v, want long (forward Item.level getter)", def.Body)
		}
	}
}

func TestGetterProjectionOnEnum(t *testing.T) {
	// A getter on an enum is a readable member too: projecting it in value position
	// is the getter's result type, not an unknown enum member.
	src := "pub enum R { A } impl {\n  pub get code(): long { return 0 }\n}\n" +
		"assert R.code == long\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (enum getter projection), got %v", codes(diags))
	}
}

func TestInheritedMethodNotAType(t *testing.T) {
	// A method reached through a nominal alias (inherited, not declared directly on
	// the alias) is a value member, not a type: member_is_not_a_type, the same as a
	// directly-declared method.
	src := "pub type Base = sbyte impl {\n  pub fn calc(): nint { return 1 }\n}\n" +
		"pub type Alias = Base\n" +
		"pub type X = Alias.calc\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMemberIsNotAType) {
		t.Fatalf("want member_is_not_a_type (inherited method), got %v", codes(diags))
	}
}

func TestGetterProjectionThroughConcreteAlias(t *testing.T) {
	// A concrete alias of a generic application (StringBox = Box<string>) composes
	// the application's substitution, so projecting (and reading) the inherited
	// getter yields the instantiated type, not the free parameter.
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type StringBox = Box<string>\n" +
		"pub type S = StringBox.item\n" +
		"assert StringBox.item == string\n" +
		"pub fn read(x: StringBox): string { return x.item }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (alias getter composes T=string), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "S" && (def.Body == nil || def.Body.String() != "string") {
			t.Fatalf("S = %v, want string (StringBox.item)", def.Body)
		}
	}
}

func TestGetterProjectionForwardBareGenericRejected(t *testing.T) {
	// A getter on a generic type declared later, projected without an application,
	// is rejected (deferred), not silently projected as if concrete.
	src := "pub type C = Box.count\n" +
		"pub type Box<T> = { v: T } impl {\n  pub get count(): nint { return 0 }\n}\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatalf("want the forward bare-generic getter projection rejected, got clean")
	}
}

func TestGetterProjectionGenericAliasNoFreeVar(t *testing.T) {
	// A getter reached through an applied generic alias (Alias<U> = Box<U>) whose
	// substitution does not compose to a concrete type is rejected, not reified as
	// a free type variable.
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Alias<U> = Box<U>\n" +
		"pub type S = { v: Alias.item<string> }\n"
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatalf("want the generic-alias getter projection rejected (no free T), got clean")
	}
}
