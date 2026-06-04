// Package types is masterbelt's type algebra: the rules and operations over a
// type value. ir owns the type representation — ir.Type and its constants are
// the data each constant is tagged with — while this package owns everything
// that reasons about that value: the classification predicates (IsInteger,
// IsBoolean, IsUntyped), Default, the lookup of builtin types by name (Lookup),
// the value-range check (Fits), the operator-method type rules (MethodResult),
// and kind compatibility (Compatible).
//
// It is deliberately syntax-free: it depends on ir (the type data) and math/big,
// and on nothing else of masterbelt's. The AST-driven half of the type system —
// inferring a type from an expression or declaration, and checking an expression
// for type errors — lives in the subpackage types/infer, which depends on this
// package and on ast.
package types

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// Default returns the concrete type an untyped constant takes when no annotation
// forces another; every concrete type is its own default.
func Default(t ir.Type) ir.Type {
	switch t {
	case ir.UntypedInt:
		return ir.Int64
	case ir.UntypedBool:
		return ir.Bool
	default:
		return t
	}
}

// IsInteger reports whether t is an integer type (untyped or concrete).
func IsInteger(t ir.Type) bool {
	return t == ir.UntypedInt || (ir.Int8 <= t && t <= ir.Uint64)
}

// IsBoolean reports whether t is a boolean type (untyped or concrete).
func IsBoolean(t ir.Type) bool {
	return t == ir.UntypedBool || t == ir.Bool
}

// IsUntyped reports whether t is an untyped constant type.
func IsUntyped(t ir.Type) bool {
	return t == ir.UntypedInt || t == ir.UntypedBool
}

// namedTypes maps the concrete type names that may appear in an annotation to
// their ir.Type. UntypedInt and Invalid are not nameable.
var namedTypes = map[string]ir.Type{
	"int8":   ir.Int8,
	"int16":  ir.Int16,
	"int32":  ir.Int32,
	"int64":  ir.Int64,
	"uint8":  ir.Uint8,
	"uint16": ir.Uint16,
	"uint32": ir.Uint32,
	"uint64": ir.Uint64,
	"bool":   ir.Bool,
}

// Lookup returns the concrete builtin type named name, or false if name is not a
// known type.
func Lookup(name string) (ir.Type, bool) {
	t, ok := namedTypes[name]
	return t, ok
}

// bounds holds the inclusive value range of a concrete integer type.
type bounds struct{ min, max *big.Int }

var typeBounds = func() map[ir.Type]bounds {
	one := big.NewInt(1)
	signed := func(bits uint) bounds {
		half := new(big.Int).Lsh(one, bits-1)
		return bounds{min: new(big.Int).Neg(half), max: new(big.Int).Sub(half, one)}
	}
	unsigned := func(bits uint) bounds {
		return bounds{min: big.NewInt(0), max: new(big.Int).Sub(new(big.Int).Lsh(one, bits), one)}
	}
	return map[ir.Type]bounds{
		ir.Int8: signed(8), ir.Int16: signed(16), ir.Int32: signed(32), ir.Int64: signed(64),
		ir.Uint8: unsigned(8), ir.Uint16: unsigned(16), ir.Uint32: unsigned(32), ir.Uint64: unsigned(64),
	}
}()

// Fits reports whether v is within the range of type t. Types without a fixed
// range — UntypedInt (arbitrary precision), the boolean types, and Invalid —
// accept any value.
func Fits(t ir.Type, v *big.Int) bool {
	b, ok := typeBounds[t]
	if !ok {
		return true
	}
	return v.Cmp(b.min) >= 0 && v.Cmp(b.max) <= 0
}

// --- operator-method type rules ---------------------------------------------

var (
	arithMethods = map[string]bool{"add": true, "sub": true, "mul": true, "div": true, "rem": true}
	orderMethods = map[string]bool{"lt": true, "lteq": true, "gt": true, "gteq": true}
	equalMethods = map[string]bool{"eql": true, "neq": true}
	logicMethods = map[string]bool{"anan": true, "oror": true}
	signMethods  = map[string]bool{"pos": true, "neg": true}
)

// MethodResult is the type rule for the builtin operator methods: arithmetic on
// integers yields an integer, the comparisons and logical operators yield a
// boolean, and the unary sign/not operators preserve their operand's type. It
// returns ir.Invalid when the method does not apply to the operand types (a type
// error), which the IR records as an Invalid type.
func MethodResult(recv ir.Type, method string, args []ir.Type) ir.Type {
	switch {
	case arithMethods[method]:
		if len(args) != 1 {
			return ir.Invalid
		}
		return unifyNumeric(recv, args[0])
	case orderMethods[method]:
		if len(args) != 1 || !IsInteger(recv) || !IsInteger(args[0]) {
			return ir.Invalid
		}
		return ir.UntypedBool
	case equalMethods[method]:
		if len(args) != 1 {
			return ir.Invalid
		}
		a := args[0]
		if (IsInteger(recv) && IsInteger(a)) || (IsBoolean(recv) && IsBoolean(a)) {
			return ir.UntypedBool
		}
		return ir.Invalid
	case logicMethods[method]:
		if len(args) != 1 {
			return ir.Invalid
		}
		return unifyBool(recv, args[0])
	case signMethods[method]:
		if len(args) != 0 || !IsInteger(recv) {
			return ir.Invalid
		}
		return recv
	case method == "not":
		if len(args) != 0 || !IsBoolean(recv) {
			return ir.Invalid
		}
		return recv
	default:
		return ir.Invalid
	}
}

// unifyNumeric is the result type of an arithmetic op on two integer types: an
// untyped operand adapts to the other, two equal types keep that type, and two
// different concrete types are a mismatch (ir.Invalid).
func unifyNumeric(a, b ir.Type) ir.Type {
	switch {
	case !IsInteger(a) || !IsInteger(b):
		return ir.Invalid
	case a == ir.UntypedInt:
		return b
	case b == ir.UntypedInt:
		return a
	case a == b:
		return a
	default:
		return ir.Invalid
	}
}

// unifyBool is the result type of a logical op on two boolean types, with the
// same untyped-adapts-to-concrete rule as unifyNumeric.
func unifyBool(a, b ir.Type) ir.Type {
	switch {
	case !IsBoolean(a) || !IsBoolean(b):
		return ir.Invalid
	case a == ir.UntypedBool:
		return b
	case b == ir.UntypedBool:
		return a
	case a == b:
		return a
	default:
		return ir.Invalid
	}
}

// Compatible reports whether an annotation and an initializer's inferred type
// agree in kind — both integer or both boolean.
func Compatible(annotation, expr ir.Type) bool {
	return (IsInteger(annotation) && IsInteger(expr)) || (IsBoolean(annotation) && IsBoolean(expr))
}
