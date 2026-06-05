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
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// Module is a resolved program: its constants, type definitions, functions,
// and compile-time assertions in source order.
type Module struct {
	Consts  []*Const
	Types   []*TypeDef
	Funcs   []*Function
	Asserts []*Assert
}

// Function is a resolved top-level function declaration: a method without a
// receiver. Like a Method it carries its signature and lowered body; FuncCall
// points at it the way Reference points at a *Const, so calls form the same
// pointer graph values do. An extern function is a native a target supplies —
// the root of an effect — and has no body. The effect list declares the
// function's interaction with the world; an empty list means pure (foldable
// at compile time).
type Function struct {
	Name    string
	Public  bool
	Extern  bool     // declared extern: a native the target supplies, no body
	Effects []string // the declared effects in source order, or nil for pure
	Doc     []string
	Params  []Param
	Result  Type
	Body    []Stmt
	Syntax  *ast.FuncDecl // the declaration this was resolved from
}

// Assert is one compile-time assertion's outcome: the condition in canonical
// surface syntax, its folded value (nil when it cannot fold), and the
// power-assert diagram of its sub-expression values — precomputed here so the
// editor's hover and the failure diagnostic render the very values the
// assertion was checked with.
type Assert struct {
	Cond    string          // canonical surface rendering of the condition
	Doc     []string        // the doc comment — the invariant in the author's words
	Eval    *Constant       // the folded condition, or nil if it could not be evaluated
	Diagram string          // the condition line plus the pipe/value rows
	Syntax  *ast.AssertDecl // the declaration this was checked from
}

// Held reports whether the assertion folded to true.
func (a *Assert) Held() bool { return a.Eval != nil && a.Eval.Kind == ConstBool && a.Eval.Bool }

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

// StringLiteral is a string literal. Value is the decoded string; the evaluated
// value lives on Const.Eval (the same string).
type StringLiteral struct {
	Value string
}

func (*StringLiteral) value() {}

// BoolLiteral is a boolean literal, true or false.
type BoolLiteral struct {
	Value bool
}

func (*BoolLiteral) value() {}

// DatetimeLiteral is a datetime literal. Its Text is the literal as written;
// the normalized UTC instant lives on Const.Eval.
type DatetimeLiteral struct {
	Text string
}

func (*DatetimeLiteral) value() {}

// DurationLiteral is a duration literal. Its Text is the literal as written;
// the totalled milliseconds live on Const.Eval.
type DurationLiteral struct {
	Text string
}

func (*DurationLiteral) value() {}

// CollectionLiteral is a list or map literal. A list's entries each carry only a
// Value; a map's entries each carry a Key and a Value. An empty literal has no
// entries; its kind comes from the constant's type.
type CollectionLiteral struct {
	Entries []CollectionEntry
}

func (*CollectionLiteral) value() {}

// CollectionEntry is one entry of a CollectionLiteral: a Value, and for a map a
// Key (nil for a list element).
type CollectionEntry struct {
	Key   Value // nil for a list element
	Value Value
}

// RecordValue is a record literal: its named type ("" for the inferred form,
// whose type comes from the constant's Type) and its field values in source
// order. The evaluated, canonically ordered value lives on Const.Eval.
type RecordValue struct {
	TypeName string
	Fields   []RecordField
}

func (*RecordValue) value() {}

// RecordField is one field initializer of a RecordValue: a name and its value.
type RecordField struct {
	Name  string
	Value Value
}

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

// FuncCall is a resolved call of a top-level function: the function it
// resolves to and the argument values.
type FuncCall struct {
	Target *Function
	Args   []Value
}

func (*FuncCall) value() {}

// FuncLiteral is a function-literal value: its parameter names and its lowered
// statement body. Like the rest of the value graph it is untyped — the
// expression's type lives on the constant's Type and in the type system — so it
// carries names and a body, not resolved parameter types.
type FuncLiteral struct {
	Params []string
	Body   []Stmt
}

func (*FuncLiteral) value() {}

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

// Await is an await expression: the explicit suspension point that consumes
// the async effect at a call site. It wraps the awaited value and adds
// nothing to its type.
type Await struct {
	Value Value
}

func (*Await) value() {}

// Ternary is a resolved conditional value, cond ? then : else: it yields Then
// when Cond holds and Else otherwise. It is the value form of a two-way choice
// (the if statement's expression counterpart); only the taken branch is
// evaluated, so it keeps its own node rather than lowering to a call.
type Ternary struct {
	Cond Value
	Then Value
	Else Value
}

func (*Ternary) value() {}

// NullValue is the null literal.
type NullValue struct{}

func (*NullValue) value() {}

// EnumMemberValue is a resolved reference to an enum member, whether written
// qualified (Rarity.Common) or bare (Common, under an enum expectation). Def is
// the enum definition and Index the member's position within it; the name and
// the base value are read from Def.Enum.Members[Index]. The evaluated value
// (an EnumConstant) lives on Const.Eval, as for every other value form.
type EnumMemberValue struct {
	Def   *TypeDef
	Index int
}

func (*EnumMemberValue) value() {}

// AssocConstValue is a resolved reference to a type's associated constant,
// written TypeName.Name (int8.Max, Level.Max). Def is the owning type and Index
// the constant's position in Def.Consts; the name, type, and folded value are
// read from Def.Consts[Index]. The evaluated value lives on Const.Eval, as for
// every other value form.
type AssocConstValue struct {
	Def   *TypeDef
	Index int
}

func (*AssocConstValue) value() {}
