package ir

import (
	"reflect"
	"testing"
)

// TestValueKindsRegistryComplete asserts ValueKinds() lists exactly the types
// in this package that implement Value, discovered by scanning the package
// source for `value()` methods — the Value twin of the Stmt registry guard, so
// a new value form cannot be quietly dropped by the dump, the walkers, or the
// interpreter's exhaustiveness pins.
func TestValueKindsRegistryComplete(t *testing.T) {
	registered := map[string]bool{}
	for _, v := range ValueKinds() {
		registered[reflect.TypeOf(v).Elem().Name()] = true
	}

	actual := implementersInSource(t, "value")
	if len(actual) == 0 {
		t.Fatal("found no value() implementers in the package source; the scan is broken")
	}
	for name := range actual {
		if !registered[name] {
			t.Errorf("type %s implements Value but is missing from ValueKinds()", name)
		}
	}
	for name := range registered {
		if !actual[name] {
			t.Errorf("ValueKinds() lists %s, which does not implement Value in the source", name)
		}
	}
}

// TestValueMarshalCoversEveryValue drives the exact text form over every value
// form; a kind without a codec already fails to build (the sealed interface
// embeds encoding.TextMarshaler), so this pins the output non-empty.
func TestValueMarshalCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		text, err := v.MarshalText()
		if err != nil {
			t.Errorf("MarshalText(%T): %v", v, err)
		}
		if len(text) == 0 {
			t.Errorf("MarshalText(%T) is empty; a value must never marshal as nothing", v)
		}
	}
}

// TestWalkValuesCoversEveryValue drives the exhaustive walker over every value
// form; an unregistered kind panics there by design, so this pins the walker's
// switch complete.
func TestWalkValuesCoversEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("WalkValues panicked on %T: %v", v, r)
				}
			}()
			WalkValues(v, func(Value) bool { return true })
		}()
	}
}

// TestTypeOfAndSyntaxOfCoverEveryValue pins the uniform readings: TypeOf and
// SyntaxOf must have a case for every value form.
func TestTypeOfAndSyntaxOfCoverEveryValue(t *testing.T) {
	for _, v := range ValueKinds() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("TypeOf/SyntaxOf panicked on %T: %v", v, r)
				}
			}()
			TypeOf(v)
			SyntaxOf(v)
		}()
	}
}
