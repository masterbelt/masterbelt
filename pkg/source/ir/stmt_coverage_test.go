package ir

import (
	"reflect"
	"testing"
)

// TestStmtKindsRegistryComplete asserts StmtKinds() lists exactly the types in
// this package that implement Stmt, discovered by scanning the package source
// for `stmt()` methods. Adding a lowered statement form forces registering it,
// which feeds it through the dump coverage test below — so a new kind cannot be
// quietly dropped by a dump walker (rendering as empty in the .ir snapshot).
func TestStmtKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range StmtKinds() {
		registered[reflect.TypeOf(s).Elem().Name()] = true
	}

	actual := implementersInSource(t, "stmt")
	if len(actual) == 0 {
		t.Fatal("found no stmt() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Stmt but is missing from StmtKinds()", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("StmtKinds() lists %s, which does not implement Stmt in the source", name)
		}
	}
}

// TestStmtMarshalNeverEmpty checks the exact text form renders every lowered
// statement kind as something non-empty, so a kind cannot silently vanish from
// the snapshot oracle. (The sealed interface embeds encoding.TextMarshaler, so
// a kind without a codec already fails to build; this pins the output shape.)
func TestStmtMarshalNeverEmpty(t *testing.T) {
	for _, s := range StmtKinds() {
		text, err := s.MarshalText()
		if err != nil {
			t.Errorf("MarshalText(%T): %v", s, err)
		}
		if len(text) == 0 {
			t.Errorf("MarshalText(%T) is empty; a statement must never marshal as nothing", s)
		}
	}
}
