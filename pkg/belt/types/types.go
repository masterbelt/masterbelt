// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation (ir.Type and its variants); this
// package owns everything that reasons about a type — the classification
// predicates (IsInteger, IsBoolean), the lookup of builtin types by name
// (Lookup), the value-range check (Fits), the operator-method type rules
// (MethodResult, built from BindReceiver, Match, and Substitute, which the
// bidirectional checker also drives directly), and
// assignability/compatibility.
//
// There is no "untyped" type: an integer literal has type int (the
// arbitrary-precision integer, which adapts to any sized integer and is
// range-checked at the boundary) and a boolean literal has type bool.
//
// None of these hardcode the set of primitives: every "is this an integer", its
// value range, and the result type of an operator method is derived from the
// builtin registry (package builtin) and the method signatures it carries, so a
// primitive added to the registry and the prelude is understood here with no
// change. The AST-driven half of the type system — inferring a type from an
// expression or declaration — lives in the subpackage types/infer.
package types

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// IsInteger reports whether t is an integer type: an integer builtin (per the
// registry) or a named type whose underlying type is an integer (so a nominal
// type like `type Level = int8` is integer-like and derives int8's operators).
func IsInteger(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsInteger()
	case *ir.Named:
		return t.Def != nil && IsInteger(reg, t.Def.Body)
	}
	return false
}

// IsBoolean reports whether t is a boolean type: the boolean builtin or a named
// type whose underlying type is boolean.
func IsBoolean(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsBoolean()
	case *ir.Named:
		return t.Def != nil && IsBoolean(reg, t.Def.Body)
	}
	return false
}

// IsString reports whether t is a string type: the string builtin or a named
// type whose underlying type is a string.
func IsString(reg *builtin.Registry, t ir.Type) bool {
	switch t := t.(type) {
	case *ir.Builtin:
		n, ok := reg.Native(t.Name)
		return ok && n.IsString()
	case *ir.Named:
		return t.Def != nil && IsString(reg, t.Def.Body)
	}
	return false
}

// defaultInt is the type of an integer literal: the arbitrary-precision integer
// that adapts to any sized integer type.
const defaultInt = "nint"

// isDefaultInt reports whether t is the literal/default integer type, which
// adapts to any other integer type.
func isDefaultInt(t ir.Type) bool {
	b, ok := t.(*ir.Builtin)
	return ok && b.Name == defaultInt
}

// Lookup resolves a type name in the registry (the builtin primitives and,
// once the prelude is installed, its aliases and collections), or false if the
// name is unknown. A primitive resolves to a Builtin, any other definition to a
// Named.
func Lookup(reg *builtin.Registry, name string) (ir.Type, bool) {
	if d, ok := reg.Lookup(name); ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}, true
		}
		return &ir.Named{Def: d}, true
	}
	return ir.Invalid, false
}

// Fits reports whether v is within the value range of type t. Non-integer types
// — and integer types without a fixed range — accept any value.
func Fits(reg *builtin.Registry, t ir.Type, v *big.Int) bool {
	seen := map[*ir.TypeDef]bool{}
	for {
		switch x := t.(type) {
		case *ir.Builtin:
			if n, ok := reg.Native(x.Name); ok {
				return n.Fits(v)
			}
			return true
		case *ir.Named:
			// A named type's range is its underlying type's; the visited set
			// keeps a self-referential definition finite.
			if x.Def == nil || x.Def.Body == nil || seen[x.Def] {
				return true
			}
			seen[x.Def] = true
			t = x.Def.Body
		default:
			return true
		}
	}
}
