package semantic

// These tests exercise the resolution of type and function declarations
// (resolve.go): the body, union members, fields, constraints, and method
// results of a type declaration, and a redeclared type, all reported against
// the declarations the universe is built from.

import (
	"testing"
)

func TestUnknownTypeInDeclaration(t *testing.T) {
	// A reference to an undeclared type in a type declaration is reported.
	for _, src := range []string{
		"pub type Coin = Nope\n",            // unknown body
		"pub type Pair = int8 | Nope\n",     // unknown union member
		"pub type Rec = {\n  id: Nope\n}\n", // unknown field type
		"pub type Box<T: Nope> = T\n",       // unknown constraint
		"pub type Lvl = int8 impl {\n  pub m(): Nope {\n    return self\n  }\n}\n", // unknown result
	} {
		if _, diags := analyze(src); !hasCode(diags, CodeUnknownType) {
			t.Errorf("%q: want unknown_type, got %v", src, codes(diags))
		}
	}
	// A well-formed declaration referencing only known types has no such error.
	if _, diags := analyze("pub type Coin = int8\npub type GameValue = Coin | int8\n"); hasCode(diags, CodeUnknownType) {
		t.Errorf("known types should not be reported unknown: %v", codes(diags))
	}
}

func TestDuplicateTypeDeclaration(t *testing.T) {
	_, diags := analyze("pub type Coin = int8\npub type Coin = int16\n")
	if !hasCode(diags, CodeDuplicateDeclaration) {
		t.Errorf("want duplicate_declaration for redeclared type, got %v", codes(diags))
	}
}
