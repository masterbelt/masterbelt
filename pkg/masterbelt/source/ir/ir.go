// Package ir is the resolved, typed intermediate representation of a masterbelt
// program: every reference is bound to its declaration and every constant has an
// inferred type. It is produced from the abstract syntax tree by package
// semantic.
//
// Unlike the AST, the IR is a semantic graph rather than a tree — a Reference
// points directly at the *Const it resolves to — so it is the right shape for
// type checking and, later, evaluation and codegen.
//
// The package is split across files: this file holds the IR graph nodes
// (Module, Const, and the Value forms); type.go holds the type as data (Type and
// its name); constant.go holds the evaluated constant values (Constant). The
// rules that reason about types — inference, checking, range checks, lookup —
// live in package types, which imports ir and not the reverse.
package ir

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
)

// Module is a resolved program: its constants and type definitions in source
// order.
type Module struct {
	Consts []*Const
	Types  []*TypeDef
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

// SelfValue is the method receiver (the self keyword) inside a method body.
type SelfValue struct{}

func (*SelfValue) value() {}

// ParamRef is a use of a method parameter, by name.
type ParamRef struct {
	Name string
}

func (*ParamRef) value() {}

// FieldAccess is a record field access: Receiver.Field.
type FieldAccess struct {
	Receiver Value
	Field    string
}

func (*FieldAccess) value() {}

// Conversion is a type conversion T(Value), as written T(x) — the form a builtin
// type name takes when applied to a value.
type Conversion struct {
	Type  Type
	Value Value
}

func (*Conversion) value() {}

// NullValue is the null literal.
type NullValue struct{}

func (*NullValue) value() {}
