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
