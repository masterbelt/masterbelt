// Package ir is the resolved, typed intermediate representation of a masterbelt
// program: every reference is bound to its declaration and every constant has an
// inferred type. It is produced from the abstract syntax tree by package
// semantic.
//
// Unlike the AST, the IR is a semantic graph rather than a tree — a Reference
// points directly at the *Const it resolves to — so it is the right shape for
// type checking and, later, evaluation and codegen.
package ir

import "github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"

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
	Syntax *ast.ConstDecl // the declaration this was lowered from
}

// Value is a resolved initializer: a literal or a reference to another constant.
type Value interface {
	value()
}

// IntLiteral is an integer literal. Its Text is the literal as written; this
// layer does not yet evaluate it.
type IntLiteral struct {
	Text string
}

func (*IntLiteral) value() {}

// Reference is a use of another constant, resolved to its declaration.
type Reference struct {
	Target *Const
}

func (*Reference) value() {}

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
)

var typeNames = map[Type]string{
	Invalid:    "invalid",
	UntypedInt: "untyped int",
	Int8:       "int8",
	Int16:      "int16",
	Int32:      "int32",
	Int64:      "int64",
	Uint8:      "uint8",
	Uint16:     "uint16",
	Uint32:     "uint32",
	Uint64:     "uint64",
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
	if t == UntypedInt {
		return Int64
	}
	return t
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
}

// LookupType returns the concrete builtin type named name, or false if name is
// not a known type.
func LookupType(name string) (Type, bool) {
	t, ok := namedTypes[name]
	return t, ok
}
