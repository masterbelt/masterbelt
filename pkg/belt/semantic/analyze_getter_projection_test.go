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

func TestGetterProjectionNestedSelf(t *testing.T) {
	// A getter whose result carries self inside a constructor (list<self>) projects
	// with the receiver substituted throughout — Item.items is list<Item>, not a
	// public type still carrying the receiver-only self marker. The same receiver
	// substitution is what makes the value-position read x.items type as list<Item>.
	src := "pub type Item = { n: nint } impl {\n  pub get items(): list<self> { return [] }\n}\n" +
		"pub type Items = Item.items\n" +
		"pub fn read(x: Item): list<Item> { return x.items }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (nested self resolves to receiver), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "Items" && (def.Body == nil || def.Body.String() != "list<Item>") {
			t.Fatalf("Items = %v, want list<Item> (nested self resolved)", def.Body)
		}
	}
}

func TestGetterProjectionForwardNestedSelf(t *testing.T) {
	// The forward path (the getter's type declared after the projecting alias, read
	// from declaration syntax) substitutes the receiver for self throughout too — a
	// forward getter returning list<self> projects to list<Item>, not list<self>.
	src := "pub type Items = Item.items\n" +
		"pub type Item = { n: nint } impl {\n  pub get items(): list<self> { return [] }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward nested self resolves to receiver), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "Items" && (def.Body == nil || def.Body.String() != "list<Item>") {
			t.Fatalf("Items = %v, want list<Item> (forward nested self resolved)", def.Body)
		}
	}
}

func TestGetterProjectionForwardEnumGetter(t *testing.T) {
	// An enum carries its declaration on EnumSyntax, not Syntax, so the forward path
	// must read enum declaration syntax too: a getter on an enum declared after the
	// projecting alias projects to its result type, not type_has_no_fields.
	src := "pub type C = R.code\n" +
		"pub enum R { A } impl {\n  pub get code(): long { return 0 }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward enum getter projection), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "C" && (def.Body == nil || def.Body.String() != "long") {
			t.Fatalf("C = %v, want long (forward enum getter)", def.Body)
		}
	}
}

func TestGetterProjectionThroughGenericAliasChain(t *testing.T) {
	// A concrete alias through a chain of generic aliases (StringBox = Box<string>,
	// Box<U> = Inner<U>) carries the argument down to the getter's declaring type, so
	// StringBox.item reads Inner's T as string — symmetric with the field projection
	// StringBox.v, which already composes through the chain.
	src := "pub type Inner<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Box<U> = Inner<U>\n" +
		"pub type StringBox = Box<string>\n" +
		"pub type G = StringBox.item\n" +
		"pub fn read(x: StringBox): string { return x.item }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (chain composes T=string), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "G" && (def.Body == nil || def.Body.String() != "string") {
			t.Fatalf("G = %v, want string (StringBox.item through the chain)", def.Body)
		}
	}
}

func TestGetterProjectionAliasParamNameCollision(t *testing.T) {
	// When a generic alias reuses the declaring type's parameter name and pins the
	// underlying application concretely (Alias<T> = Box<string>), the getter sees the
	// application's argument, not the alias's: Alias.item<nint> reads string (Box's T
	// = string), not nint — the same answer the field projection Alias.v<nint> gives.
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Alias<T> = Box<string>\n" +
		"pub type G = Alias.item<nint>\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (collision resolves to the application's arg), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "G" && (def.Body == nil || def.Body.String() != "string") {
			t.Fatalf("G = %v, want string (Alias.item<nint> = Box<string>.item)", def.Body)
		}
	}
}

func TestGetterProjectionAliasDeclaredGetter(t *testing.T) {
	// A getter the generic alias declares itself reads the alias's own parameter,
	// even when the alias body is another application reusing the name: with
	// Alias<T> = Box<string> and a getter own(): list<T> on Alias, Alias.own<nint> is
	// list<nint> (Alias's T), not list<string> from Box<string> — the substitution
	// is scoped to the definition that declares the getter, so the body chain does
	// not overwrite it.
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Alias<T> = Box<string> impl {\n  pub get own(): list<T> { return [] }\n}\n" +
		"pub type G = Alias.own<nint>\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (alias-declared getter reads the alias parameter), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "G" && (def.Body == nil || def.Body.String() != "list<nint>") {
			t.Fatalf("G = %v, want list<nint> (Alias.own<nint> reads the alias's T)", def.Body)
		}
	}
}

func TestGetterProjectionForwardGeneric(t *testing.T) {
	// A getter on a generic type declared after the projecting alias — a forward
	// reference whose methods are not attached yet — is read from the declaration
	// syntax in the parameter scope and instantiated with the application's
	// argument: LateBox.item<long> is long, the getter twin of the field forward
	// generic projection (Late.value<long>), so read and projection agree even for
	// a forward generic.
	src := "pub type EarlyItem = LateBox.item<long>\n" +
		"pub type LateBox<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward generic getter projection), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "EarlyItem" && (def.Body == nil || def.Body.String() != "long") {
			t.Fatalf("EarlyItem = %v, want long (forward LateBox<long>.item)", def.Body)
		}
	}
}

func TestGetterProjectionForwardGenericNestedSelf(t *testing.T) {
	// A forward generic getter whose result carries self inside a constructor
	// (list<self>) substitutes the receiver application for self throughout, after
	// the parameter is bound: LateBox.items<long> is list<LateBox<long>>, not a type
	// still carrying the receiver-only self marker — the forward generic twin of the
	// nested-self substitution the resolved and non-generic forward paths already do.
	src := "pub type EarlyItems = LateBox.items<long>\n" +
		"pub type LateBox<T> = { v: T } impl {\n  pub get items(): list<self> { return [] }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward generic nested self), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "EarlyItems" && (def.Body == nil || def.Body.String() != "list<LateBox<long>>") {
			t.Fatalf("EarlyItems = %v, want list<LateBox<long>> (forward generic nested self)", def.Body)
		}
	}
}

func TestGetterProjectionForwardGenericValuePosition(t *testing.T) {
	// A getter projected in value position through a concrete alias of a
	// forward-declared generic (EB = LateBox<string>, LateBox declared after) is a
	// comptime type value an assert consumes: EB.item folds to string. The read
	// agrees, so x.item on an EB reads string too.
	src := "pub type EB = LateBox<string>\n" +
		"assert EB.item == string\n" +
		"pub fn read(x: EB): string { return x.item }\n" +
		"pub type LateBox<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n"
	_, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward generic getter value position), got %v", codes(diags))
	}
}

func TestGetterProjectionForwardGenericAliasChainRejected(t *testing.T) {
	// A getter reached through a forward-referenced generic-alias chain (Crate<U> =
	// Box<U>, both declared after the projecting alias) has no settled record shape
	// to read the getter from, so it is generic_type_projection — exactly what the
	// field projection Crate.value<long> reports in the same forward-chain shape.
	// Keeping it rejected preserves read/projection symmetry: neither field nor
	// getter projects through a forward generic-alias chain.
	src := "pub type EarlyItem = Crate.item<long>\n" +
		"pub type Crate<U> = Box<U>\n" +
		"pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeGenericTypeProjection) {
		t.Fatalf("want generic_type_projection (forward generic-alias chain, symmetric with field), got %v", codes(diags))
	}
}

func TestGetterProjectionForwardAliasDeclaredGetter(t *testing.T) {
	// A getter declared in a generic alias's own impl block projects even when the
	// alias is a forward reference and its body is another application rather than a
	// record (so it carries no record shape to read fields from): Alias.own<nint> is
	// list<nint>, the alias's own T, exactly as the resolved order gives
	// (TestGetterProjectionAliasDeclaredGetter). A getter declared on the definition
	// is read from its syntax independently of the body's shape, so the forward order
	// is not rejected as a forward generic-alias chain.
	src := "pub type G = Alias.own<nint>\n" +
		"pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Alias<T> = Box<string> impl {\n  pub get own(): list<T> { return [] }\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (forward alias-declared getter reads the alias's T), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "G" && (def.Body == nil || def.Body.String() != "list<nint>") {
			t.Fatalf("G = %v, want list<nint> (forward Alias.own<nint> reads the alias's T)", def.Body)
		}
	}
}

func TestGetterProjectionForwardGenericBoundEnforced(t *testing.T) {
	// A forward generic getter enforces the declaring type's parameter bound on the
	// projection argument, exactly as the forward field projection does: the
	// application is built off the shell before its Params resolve, so the
	// projection re-checks against the bound from the declaration syntax —
	// LateBox.item<{ x: nint }> before LateBox<T: comparable> is bound_not_satisfied.
	forward := "pub type S = { v: LateBox.item<{ x: nint }> }\n" +
		"pub type LateBox<T: comparable> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n"
	if _, diags := analyze(forward); !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("forward: want bound_not_satisfied, got %v", codes(diags))
	}
}

func TestGetterProjectionGenericAliasApplied(t *testing.T) {
	// A getter reached through an applied generic alias (Alias<U> = Box<U>) composes
	// the substitution through the chain: Alias.item<string> binds U = string, which
	// flows to Box's T, so the projection is string — symmetric with the field
	// projection Alias.v<string>. A free type variable with no application to pin it
	// — a bare generic Box.item — stays rejected (TestGetterProjectionBareGenericRejected).
	src := "pub type Box<T> = { v: T } impl {\n  pub get item(): T { return self.v }\n}\n" +
		"pub type Alias<U> = Box<U>\n" +
		"pub type S = { v: Alias.item<string> }\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("want clean (applied generic alias composes U=string), got %v", codes(diags))
	}
	for _, def := range m.Types {
		if def.Name == "S" {
			rec, ok := def.Body.(*ir.Record)
			if !ok || len(rec.Fields) != 1 || rec.Fields[0].Type.String() != "string" {
				t.Fatalf("S = %v, want { v: string } (Alias.item<string>)", def.Body)
			}
		}
	}
}
