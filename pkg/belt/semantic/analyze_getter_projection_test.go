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
