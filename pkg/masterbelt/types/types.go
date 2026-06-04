// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation (ir.Type and its variants); this
// package owns everything that reasons about a type — the classification
// predicates (IsInteger, IsBoolean, IsUntyped), Default, the lookup of builtin
// types by name (Lookup), the value-range check (Fits), the operator-method type
// rules (MethodResult), and assignability/compatibility.
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
// registry) or the untyped integer constant.
func IsInteger(reg *builtin.Registry, t ir.Type) bool {
	if b, ok := t.(*ir.Builtin); ok {
		if n, ok := reg.Native(b.Name); ok {
			return n.IsInteger()
		}
		return false
	}
	return t == ir.UntypedInt
}

// IsBoolean reports whether t is a boolean type: the boolean builtin or the
// untyped boolean constant.
func IsBoolean(reg *builtin.Registry, t ir.Type) bool {
	if b, ok := t.(*ir.Builtin); ok {
		if n, ok := reg.Native(b.Name); ok {
			return n.IsBoolean()
		}
		return false
	}
	return t == ir.UntypedBool
}

// IsUntyped reports whether t is an untyped constant type.
func IsUntyped(t ir.Type) bool { return t == ir.UntypedInt || t == ir.UntypedBool }

// Default returns the concrete type an untyped constant takes when no annotation
// forces another; every other type is its own default.
func Default(t ir.Type) ir.Type {
	switch t {
	case ir.UntypedInt:
		return &ir.Builtin{Name: "int64"}
	case ir.UntypedBool:
		return &ir.Builtin{Name: "bool"}
	default:
		return t
	}
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
// expected: the same type, or an untyped constant flowing into a matching
// concrete type.
func Assignable(reg *builtin.Registry, from, to ir.Type) bool {
	if from == to {
		return true
	}
	if from == ir.UntypedInt && IsInteger(reg, to) {
		return true
	}
	if from == ir.UntypedBool && IsBoolean(reg, to) {
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
// receiver's type, unifies the self-typed operands (so an untyped operand adapts
// to a concrete one), and returns the substituted result type — self for a
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
	m := findMethod(def, method)
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
// the registry definition for a builtin, the referent for a named type, and the
// canonical integer/boolean definition for an untyped constant.
func defOf(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Builtin:
		if d, ok := reg.Lookup(t.Name); ok {
			return d
		}
	case *ir.Named:
		return t.Def
	}
	switch t {
	case ir.UntypedInt:
		d, _ := reg.Lookup("int")
		return d
	case ir.UntypedBool:
		d, _ := reg.Lookup("bool")
		return d
	}
	return nil
}

func findMethod(def *ir.TypeDef, name string) *ir.Method {
	for _, m := range def.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// combine unifies two operand types under the untyped-adapts-to-concrete rule:
// an untyped constant takes the other operand's type when kinds agree, two equal
// types keep that type, and anything else is a mismatch (ir.Invalid).
func combine(reg *builtin.Registry, a, b ir.Type) ir.Type {
	switch {
	case a == b:
		return a
	case a == ir.UntypedInt:
		if IsInteger(reg, b) {
			return b
		}
	case b == ir.UntypedInt:
		if IsInteger(reg, a) {
			return a
		}
	case a == ir.UntypedBool:
		if IsBoolean(reg, b) {
			return b
		}
	case b == ir.UntypedBool:
		if IsBoolean(reg, a) {
			return a
		}
	case sameBuiltin(a, b):
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
