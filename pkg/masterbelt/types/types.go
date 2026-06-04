// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation (ir.Type and its variants); this
// package owns everything that reasons about a type — the classification
// predicates (IsInteger, IsBoolean), the lookup of builtin types by name
// (Lookup), the value-range check (Fits), the operator-method type rules
// (MethodResult), and assignability/compatibility.
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

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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

// defaultInt is the type of an integer literal: the arbitrary-precision integer
// that adapts to any sized integer type.
const defaultInt = "int"

// isDefaultInt reports whether t is the literal/default integer type, which
// adapts to any other integer type.
func isDefaultInt(t ir.Type) bool {
	b, ok := t.(*ir.Builtin)
	return ok && b.Name == defaultInt
}

// Lookup resolves a builtin type name to its type, or false if name is not a
// known builtin. (User-declared types are resolved by the analyzer's type
// universe, not here.)
func Lookup(reg *builtin.Registry, name string) (ir.Type, bool) {
	if _, ok := reg.Lookup(name); ok {
		return &ir.Builtin{Name: name}, true
	}
	return ir.Invalid, false
}

// Fits reports whether v is within the value range of type t. Non-integer types
// — and integer types without a fixed range — accept any value.
func Fits(reg *builtin.Registry, t ir.Type, v *big.Int) bool {
	if b, ok := t.(*ir.Builtin); ok {
		if n, ok := reg.Native(b.Name); ok {
			return n.Fits(v)
		}
	}
	return true
}

// Compatible reports whether an annotation and an initializer's inferred type
// agree in kind — both integer or both boolean.
func Compatible(reg *builtin.Registry, annotation, expr ir.Type) bool {
	return (IsInteger(reg, annotation) && IsInteger(reg, expr)) ||
		(IsBoolean(reg, annotation) && IsBoolean(reg, expr))
}

// Assignable reports whether a value of type from may be used where type to is
// expected: the same type, or the default integer flowing into any other
// integer type (range-checked at the boundary, so an overflow is reported
// separately).
func Assignable(reg *builtin.Registry, from, to ir.Type) bool {
	if from == to {
		return true
	}
	if isDefaultInt(from) && IsInteger(reg, to) {
		return true
	}
	if a, ok := from.(*ir.Builtin); ok {
		if b, ok := to.(*ir.Builtin); ok {
			return a.Name == b.Name
		}
	}
	return false
}

// MethodResult is the type rule for a method call: it finds the method on the
// receiver's type, unifies the self-typed operands (so the default integer
// adapts to a sized one), and returns the substituted result type — self for a
// self-returning method, the declared result otherwise. It returns ir.Invalid
// when the method does not exist on the receiver or the operands do not fit,
// which the IR records as an Invalid type.
//
// Because the method signatures come from the registry's type definitions (and,
// once loaded, the prelude's), this one rule covers every operator on every
// primitive — there is no per-operator table.
func MethodResult(reg *builtin.Registry, recv ir.Type, method string, args []ir.Type) ir.Type {
	def := defOf(reg, recv)
	if def == nil {
		return ir.Invalid
	}
	m := findMethod(reg, def, method)
	if m == nil || len(args) != len(m.Params) {
		return ir.Invalid
	}

	operand := recv // the unified type of the receiver and the self-typed args
	for i, p := range m.Params {
		if _, isSelf := p.Type.(*ir.SelfType); isSelf {
			operand = combine(reg, operand, args[i])
			if operand == ir.Invalid {
				return ir.Invalid
			}
		} else if !Assignable(reg, args[i], p.Type) {
			return ir.Invalid
		}
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return operand
	}
	return m.Result
}

// defOf returns the type definition whose methods apply to a value of type t:
// the registry definition for a builtin, the referent for a named type.
func defOf(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Builtin:
		if d, ok := reg.Lookup(t.Name); ok {
			return d
		}
	case *ir.Named:
		return t.Def
	}
	return nil
}

// findMethod looks up a method by name on def, deriving from the underlying type
// when def does not declare it itself: a nominal type (type Level = int8) thus
// inherits the operator methods of its underlying type. The seen set guards
// against a cyclic definition.
func findMethod(reg *builtin.Registry, def *ir.TypeDef, name string) *ir.Method {
	return findMethodSeen(reg, def, name, map[*ir.TypeDef]bool{})
}

func findMethodSeen(reg *builtin.Registry, def *ir.TypeDef, name string, seen map[*ir.TypeDef]bool) *ir.Method {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	for _, m := range def.Methods {
		if m.Name == name {
			return m
		}
	}
	// Derive from the underlying type, unless this is a primitive (whose body is
	// itself) or has no underlying definition.
	if !def.Builtin {
		if ud := defOf(reg, def.Body); ud != nil {
			return findMethodSeen(reg, ud, name, seen)
		}
	}
	return nil
}

// combine unifies two operand types: the default integer adapts to the other
// integer operand, two equal types keep that type, and anything else is a
// mismatch (ir.Invalid). It is how an integer literal takes the type of the
// sized integer it is combined with.
func combine(reg *builtin.Registry, a, b ir.Type) ir.Type {
	switch {
	case a == b:
		return a
	case isDefaultInt(a) && IsInteger(reg, b):
		return b
	case isDefaultInt(b) && IsInteger(reg, a):
		return a
	case sameBuiltin(a, b), sameNamed(a, b):
		return a
	}
	return ir.Invalid
}

func sameBuiltin(a, b ir.Type) bool {
	x, ok := a.(*ir.Builtin)
	if !ok {
		return false
	}
	y, ok := b.(*ir.Builtin)
	return ok && x.Name == y.Name
}

func sameNamed(a, b ir.Type) bool {
	x, ok := a.(*ir.Named)
	if !ok {
		return false
	}
	y, ok := b.(*ir.Named)
	return ok && x.Def == y.Def
}
