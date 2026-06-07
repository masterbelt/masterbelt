package ir

import (
	"reflect"
	"testing"
)

// TestLinkCoversEveryValue drives the relink walk over every value form: the
// linker is an exhaustive switch guarded by a panic, and this pin turns an
// unhandled form into a failing test rather than a runtime crash on the first
// module that carries it. (Minimal instances have nil references, so the
// dispatch is what is exercised — exactly like the interpreter's coverage
// pin.)
func TestLinkCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", v, r)
				}
			}()
			m := &Module{Consts: []*Const{{Name: "X", Value: v}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", v, err)
			}
		}()
	}
}

// TestLinkCoversEveryStmt is the statement half of the linker pin.
func TestLinkCoversEveryStmt(t *testing.T) {
	for _, s := range StmtKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Link panicked on %T: %v", s, r)
				}
			}()
			m := &Module{Funcs: []*Function{{Name: "f", Body: []Stmt{s}}}}
			if err := m.Link(Resolver{}); err != nil {
				t.Errorf("Link(%T) = %v", s, err)
			}
		}()
	}
}

// TestTypeCodecCoversEveryType drives every Type form through the four
// hand-written dispatch switches — heading, marshal, decode, and relink — so
// a form added to the sealed interface without teaching the codec fails here
// instead of panicking on the first module that carries it.
func TestTypeCodecCoversEveryType(t *testing.T) {
	for _, typ := range TypeKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the type codec panicked on %T: %v", typ, r)
				}
			}()
			text, err := typ.MarshalText()
			if err != nil {
				t.Errorf("MarshalText(%T): %v", typ, err)
				return
			}
			if len(text) == 0 {
				t.Errorf("MarshalText(%T) is empty", typ)
				return
			}
			m := &Module{Consts: []*Const{{Name: "X", Type: typ}}}
			data, err := m.MarshalText()
			if err != nil {
				t.Errorf("module marshal with a %T: %v", typ, err)
				return
			}
			var back Module
			if err := back.UnmarshalText(data); err != nil {
				t.Errorf("module with a %T does not unmarshal: %v", typ, err)
				return
			}
			if err := back.Link(Resolver{TypeDef: func(string) *TypeDef { return &TypeDef{} }}); err != nil {
				t.Errorf("module with a %T does not link: %v", typ, err)
			}
		}()
	}
}

// TestTypeKindsRegistryComplete asserts TypeKinds() lists exactly the types
// in this package that satisfy the Type interface, discovered by scanning the
// package source for typ() implementers — the same cross-check StmtKinds and
// ValueKinds carry, extended to the hand-written codec's sealed set.
func TestTypeKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, typ := range TypeKinds() {
		name := reflect.TypeOf(typ).Elem().Name() // *Builtin -> Builtin, *invalid -> invalid
		if registered[name] {
			t.Errorf("TypeKinds() lists %s more than once", name)
		}
		registered[name] = true
	}

	actual := implementersInSource(t, "typ")
	if len(actual) == 0 {
		t.Fatal("found no typ() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Type but is missing from TypeKinds() — add it so the codec pins cover it", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("TypeKinds() lists %s, which does not implement Type in the package source", name)
		}
	}
}
