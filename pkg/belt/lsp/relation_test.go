package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

// the master the relation-completion tests offer members of.
const relationMaster = "master Cards {\n" +
	"  record { id: int, cost: int } impl {\n" +
	"    pub static fn zero(): nint {\n      return 0\n    }\n" +
	"  }\n" +
	"  primary id\n}\n"

// TestCompletionRelationMethods pins that a bare master name in value position offers
// its relation's query methods (where, count, sum) — the master is its relation —
// alongside its static fns, which are reached through the same name.
func TestCompletionRelationMethods(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	items := byLabel(completion(doc, off).Items)
	for _, m := range []string{"where", "count", "sum"} {
		it, ok := items[m]
		if !ok {
			t.Errorf("Cards. should offer the relation method %q: %v", m, labels(items))
			continue
		}
		if it.Kind == nil || *it.Kind != protocol.CompletionItemKindMethod {
			t.Errorf("%s kind = %v, want Method", m, it.Kind)
		}
	}
	// The master's static fn is offered too: a master is both a relation value and a
	// type with static fns.
	if _, ok := items["zero"]; !ok {
		t.Errorf("Cards. should also offer the static fn zero: %v", labels(items))
	}
}

// TestHoverRelationMethod pins that a relation method call on a bare master name
// hovers with the method's signature — the receiver resolves to relation<Cards>, so
// count is a known method, not an unresolved member.
func TestHoverRelationMethod(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.count()\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on the relation method count")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover for count should name it: %q", h.Contents.Value)
	}
}

// TestCompletionStaticFnShadowsRelationMethod pins that a master's static fn named
// like a relation method (count) wins: count is offered once, as the static fn (the
// checker resolves the static call first), while the unshadowed relation methods
// (where, sum) still appear.
func TestCompletionStaticFnShadowsRelationMethod(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn count(): nint {\n      return 0\n    }\n" +
		"  }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	all := completion(doc, off).Items
	n := 0
	var countKind *protocol.CompletionItemKind
	for _, it := range all {
		if it.Label == "count" {
			n++
			countKind = it.Kind
		}
	}
	if n != 1 {
		t.Errorf("count should appear once (the static fn shadows the relation method), got %d", n)
	}
	if countKind == nil || *countKind != protocol.CompletionItemKindFunction {
		t.Errorf("count should be the static fn (Function), got kind %v", countKind)
	}
	if _, ok := byLabel(all)["where"]; !ok {
		t.Errorf("the unshadowed relation method where should still be offered: %v", labels(byLabel(all)))
	}
}

// TestCompletionLocalShadowsMaster pins that a body-local of a master's name shadows
// it: Cards. after let Cards = ... does not advertise the relation methods, since the
// expression refers to the local, not the relation.
func TestCompletionLocalShadowsMaster(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): nint {\n  let Cards = 5\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.LastIndex(src, "Cards.") + len("Cards.")
	items := byLabel(completion(doc, off).Items)
	for _, m := range []string{"where", "count", "sum"} {
		if _, ok := items[m]; ok {
			t.Errorf("a local Cards shadows the master; %q must not be offered: %v", m, labels(items))
		}
	}
}

// TestHoverNoRelationInConst pins that a master in a constant initializer is not read
// as a relation: a const cannot evaluate one, so the checker keeps it a metatype and
// hover does not present count as a relation method.
func TestHoverNoRelationInConst(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"const X = Cards.count()\n"
	doc := testView(src)
	if h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards.")); h != nil {
		t.Errorf("count in a const initializer is not a relation method; want no hover, got %q", h.Contents.Value)
	}
}
