// Package ir is the resolved, typed intermediate representation of a masterbelt
// program: every reference is bound to its declaration and every constant has an
// inferred type. It is produced from the abstract syntax tree by package
// semantic.
//
// Unlike the AST, the IR is a semantic graph rather than a tree — a Reference
// points directly at the *Const it resolves to — so it is the right shape for
// type checking and, later, evaluation and codegen.
package ir

import (
	"math/big"
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
)

// Module is a resolved program: its constants in source order.
type Module struct {
	Consts []*Const
}

// Const is a resolved constant declaration.
type Const struct {
	Name   string // the declared name ("" if the source omitted it)
	Public bool   // whether it is marked pub
	Doc    []string
	Type   Type           // the inferred or annotated type
	Value  Value          // the resolved initializer, or nil if missing/invalid
	Eval   *Constant      // the evaluated value, or nil if it could not be evaluated
	Syntax *ast.ConstDecl // the declaration this was lowered from
}

// Value is a resolved initializer: a literal or a reference to another constant.
type Value interface {
	value()
}

// IntLiteral is an integer literal. Its Text is the literal as written; the
// evaluated value lives on Const.Eval.
type IntLiteral struct {
	Text string
}

func (*IntLiteral) value() {}

// BoolLiteral is a boolean literal, true or false.
type BoolLiteral struct {
	Value bool
}

func (*BoolLiteral) value() {}

// Reference is a use of another constant, resolved to its declaration.
type Reference struct {
	Target *Const
}

func (*Reference) value() {}

// Call is a resolved method call, the form every operator desugars to: the
// receiver, the method name, and the argument values (one for a binary
// operator, none for a unary). Receiver and arguments are themselves resolved
// values, so a Call is the whole operator expression with references bound.
type Call struct {
	Receiver Value
	Method   string
	Args     []Value
}

func (*Call) value() {}

// Type is a masterbelt type. Integer literals are untyped constants
// (UntypedInt) whose default type is Int64; an annotation gives a constant a
// concrete type.
type Type int

const (
	Invalid    Type = iota // could not be determined (unknown type name, cycle, ...)
	UntypedInt             // an un-annotated integer constant; defaults to Int64
	Int8
	Int16
	Int32
	Int64
	Uint8
	Uint16
	Uint32
	Uint64
	UntypedBool // an un-annotated boolean constant; defaults to Bool
	Bool
)

var typeNames = map[Type]string{
	Invalid:     "invalid",
	UntypedInt:  "untyped int",
	Int8:        "int8",
	Int16:       "int16",
	Int32:       "int32",
	Int64:       "int64",
	Uint8:       "uint8",
	Uint16:      "uint16",
	Uint32:      "uint32",
	Uint64:      "uint64",
	UntypedBool: "untyped bool",
	Bool:        "bool",
}

// String returns the type's name.
func (t Type) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return "Type(?)"
}

// Default returns the concrete type an untyped constant takes when no annotation
// forces another; every concrete type is its own default.
func (t Type) Default() Type {
	switch t {
	case UntypedInt:
		return Int64
	case UntypedBool:
		return Bool
	default:
		return t
	}
}

// IsInteger reports whether t is an integer type (untyped or concrete).
func (t Type) IsInteger() bool {
	return t == UntypedInt || (Int8 <= t && t <= Uint64)
}

// IsBoolean reports whether t is a boolean type (untyped or concrete).
func (t Type) IsBoolean() bool {
	return t == UntypedBool || t == Bool
}

// IsUntyped reports whether t is an untyped constant type.
func (t Type) IsUntyped() bool {
	return t == UntypedInt || t == UntypedBool
}

// namedTypes maps the concrete type names that may appear in an annotation to
// their Type. UntypedInt and Invalid are not nameable.
var namedTypes = map[string]Type{
	"int8":   Int8,
	"int16":  Int16,
	"int32":  Int32,
	"int64":  Int64,
	"uint8":  Uint8,
	"uint16": Uint16,
	"uint32": Uint32,
	"uint64": Uint64,
	"bool":   Bool,
}

// LookupType returns the concrete builtin type named name, or false if name is
// not a known type.
func LookupType(name string) (Type, bool) {
	t, ok := namedTypes[name]
	return t, ok
}

// bounds holds the inclusive value range of a concrete integer type.
type bounds struct{ min, max *big.Int }

var typeBounds = func() map[Type]bounds {
	one := big.NewInt(1)
	signed := func(bits uint) bounds {
		half := new(big.Int).Lsh(one, bits-1)
		return bounds{min: new(big.Int).Neg(half), max: new(big.Int).Sub(half, one)}
	}
	unsigned := func(bits uint) bounds {
		return bounds{min: big.NewInt(0), max: new(big.Int).Sub(new(big.Int).Lsh(one, bits), one)}
	}
	return map[Type]bounds{
		Int8: signed(8), Int16: signed(16), Int32: signed(32), Int64: signed(64),
		Uint8: unsigned(8), Uint16: unsigned(16), Uint32: unsigned(32), Uint64: unsigned(64),
	}
}()

// Fits reports whether v is within the range of type t. Types without a fixed
// range — UntypedInt (arbitrary precision), the boolean types, and Invalid —
// accept any value.
func (t Type) Fits(v *big.Int) bool {
	b, ok := typeBounds[t]
	if !ok {
		return true
	}
	return v.Cmp(b.min) >= 0 && v.Cmp(b.max) <= 0
}

// ConstKind distinguishes the two kinds of evaluated constant value.
type ConstKind int

const (
	ConstInt  ConstKind = iota // an arbitrary-precision integer (Constant.Int)
	ConstBool                  // a boolean (Constant.Bool)
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer or a boolean. A nil *Constant means "could not be
// evaluated" — a missing initializer, an undefined reference, a cycle, a type
// error, or a division by zero.
type Constant struct {
	Kind ConstKind
	Int  *big.Int // valid when Kind == ConstInt
	Bool bool     // valid when Kind == ConstBool
}

// IntConstant builds an integer constant.
func IntConstant(n *big.Int) *Constant { return &Constant{Kind: ConstInt, Int: n} }

// BoolConstant builds a boolean constant.
func BoolConstant(b bool) *Constant { return &Constant{Kind: ConstBool, Bool: b} }

// String renders the constant's value: the integer, or "true"/"false".
func (c *Constant) String() string {
	if c == nil {
		return "<unevaluated>"
	}
	if c.Kind == ConstBool {
		return strconv.FormatBool(c.Bool)
	}
	return c.Int.String()
}
