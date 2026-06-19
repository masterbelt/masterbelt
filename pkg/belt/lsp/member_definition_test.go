package lsp

import (
	"strings"
	"testing"
)

// memberDefSrc declares a master with a static fn and a scope entry, a plain type
// with a value method, and a function that calls each — so every user-declared
// member kind has both a declaration and a call site to navigate between.
const memberDefSrc = "master Cards {\n" +
	"  record { id: int, cost: int } impl {\n" +
	"    pub static fn zero(): nint {\n      return 0\n    }\n" +
	"  }\n" +
	"  scope {\n    pub expensive() -> where(fn(c) -> c.cost > 100)\n  }\n" +
	"  primary id\n}\n" +
	"pub type Counter = int impl {\n  pub fn inc(): self {\n    return self\n  }\n}\n" +
	"fn probe(x: Counter): nint {\n" +
	"  let a = Cards.zero()\n" +
	"  let b = Cards.expensive()\n" +
	"  let d = x.inc()\n" +
	"  return a\n}\n"

// TestDefinitionMemberMethod pins go-to-definition on a member-access method call:
// a master's static fn (Cards.zero()), a scope entry desugared to one
// (Cards.expensive()), and a value method on a plain type (x.inc()) each jump to
// their declaration name, resolved through the receiver's type — the same resolution
// hover uses, now wired into definition.
func TestDefinitionMemberMethod(t *testing.T) {
	doc := testView(memberDefSrc)
	buf := doc.Buffer()
	for _, c := range []struct{ name, call, declName string }{
		{"static fn", "Cards.zero()", "zero"},
		{"scope entry", "Cards.expensive()", "expensive"},
		{"value method", "x.inc()", "inc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			off := strings.Index(memberDefSrc, c.call) + strings.Index(c.call, ".") + 1
			locs := definition(doc, off)
			if len(locs) != 1 {
				t.Fatalf("definition(%s) = %d locations, want 1", c.call, len(locs))
			}
			start := fromPosition(buf, locs[0].Range.Start)
			end := fromPosition(buf, locs[0].Range.End)
			if got := memberDefSrc[start:end]; got != c.declName {
				t.Errorf("definition(%s) covers %q, want the declaration name %q", c.call, got, c.declName)
			}
			// It must jump to the declaration, which precedes the call site, not stay
			// on the call's own member token.
			if start >= off {
				t.Errorf("definition(%s) stayed at/after the call (offset %d >= %d); want the earlier declaration", c.call, start, off)
			}
		})
	}
}

// TestDefinitionRelationBuiltinHasNoLocation pins that a relation builtin (count,
// assembled from the prelude) has no navigable declaration: it resolves to zero
// locations rather than a phantom position, since the prelude is in no workspace file.
func TestDefinitionRelationBuiltinHasNoLocation(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.count()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.count()") + len("Cards.")
	if locs := definition(doc, off); len(locs) != 0 {
		t.Errorf("a relation builtin has no navigable declaration; want 0 locations, got %d: %+v", len(locs), locs)
	}
}
