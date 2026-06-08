// Package ir is the resolved, typed intermediate representation of a masterbelt
// program: every reference is bound to its declaration and every constant has an
// inferred type. It is produced from the abstract syntax tree by package
// semantic.
//
// Unlike the AST, the IR is a semantic graph rather than a tree — a Reference
// points directly at the *Const it resolves to — so it is the right shape for
// type checking and, later, evaluation and codegen.
//
// The Syntax backpointers the nodes carry (a declaration's, a value node's)
// are editor and diagnostic anchors — positions, hovers, the write-back's
// pairing keys — and never carry semantics: everything a consumer of the IR
// needs to know is on the IR's own fields, so the graph (plus the builtin
// registry's native table) is a complete input on its own.
//
// The package is split across files: this file holds the IR graph nodes
// (Module, Const, and the Value forms); type.go holds the type as data (Type and
// its name); constant.go holds the evaluated constant values (Constant). The
// rules that reason about types — inference, checking, range checks, lookup —
// live in package types, which imports ir and not the reverse.
package ir

//go:generate go run github.com/masterbelt/masterbelt/pkg/source/internal/treegen -marshal Value,Stmt -roots Module,Constant -custom Type -out text_gen.go

import (
	"encoding"
	"fmt"

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
	Name string
	// Anchor is the declaration's stable, position-independent address
	// (belt:module/name) — the same string across edits that do not rename it
	// or move its file. It is "" for an unnamed declaration, which has no
	// address; the semantic layer stamps it, deterministically from the name
	// and the file path, so it survives incremental recompute unchanged.
	Anchor  string
	Public  bool
	Extern  bool     // declared extern: a native the target supplies, no body
	Effects []string // the declared effects in source order, or nil for pure
	Doc     []string
	// TypeParams are the function's generic type parameters (the T in
	// f<T: foldable<int>>), each a name with an optional resolved interface
	// bound. A parameter or the result whose type names one of them is a
	// TypeVar; a call resolves each from the argument types and folds with the
	// concrete type. Empty for a non-generic function.
	TypeParams []*TypeParam
	Params     []Param
	Result     Type
	Body       []Stmt
	Syntax     *ast.FuncDecl `tree:"-"` // the declaration this was resolved from
}

// Assert is one compile-time assertion's outcome: the condition in canonical
// surface syntax, its folded value (nil when it cannot fold), and the
// power-assert diagram of its sub-expression values — precomputed here so the
// editor's hover and the failure diagnostic render the very values the
// assertion was checked with.
type Assert struct {
	Cond    string    // canonical surface rendering of the condition
	Doc     []string  // the doc comment — the invariant in the author's words
	Eval    *Constant // the folded condition, or nil if it could not be evaluated
	Diagram string    // the condition line plus the pipe/value rows
	// CondGraph is the resolved condition value graph — references bound, self
	// resolved — kept in memory so a consumer can traverse what the assertion
	// uses: the reachability lint reads it to keep a constant an assert exercises
	// live, and find-references reaches an assert's uses through it. It is
	// tree:"-", so the text form renders the outcome (Cond, Eval, Diagram), not
	// the graph; a module read back from text has it nil, as it does the Syntax.
	CondGraph Value           `tree:"-"`
	Syntax    *ast.AssertDecl `tree:"-"` // the declaration this was checked from
}

// Held reports whether the assertion folded to true.
func (a *Assert) Held() bool { return a.Eval != nil && a.Eval.Kind == ConstBool && a.Eval.Bool }

// Const is a resolved constant declaration.
type Const struct {
	Name string // the declared name ("" if the source omitted it)
	// Anchor is the constant's stable, position-independent address
	// (belt:module/name); see Function.Anchor. "" for an unnamed constant.
	Anchor string
	Public bool // whether it is marked pub
	Doc    []string
	Type   Type           // the inferred or annotated type
	Value  Value          // the resolved initializer, or nil if missing/invalid
	Eval   *Constant      // the evaluated value, or nil if it could not be evaluated
	Syntax *ast.ConstDecl `tree:"-"` // the declaration this was lowered from
}

// Value is a resolved initializer: a literal or a reference to another constant.
//
// Every value node carries a Type — its checker-settled type, the typed value
// graph. A node is born with it nil at lowering (which is
// type-blind) and the post-check write-back fills it: a literal at its
// synthesized type (an integer literal is nint even where a sized type expects
// it — the width settle is an explicit adaption, not a retype), a reference at
// its binding's type, a call at the checker's result. A node in a declaration
// the checker never settled (a broken file) keeps nil, which the dump renders
// bare — the hole is visible, never invented.
//
// Every value form marshals to the exact text representation (text_gen.go);
// embedding encoding.TextMarshaler makes that a compile-time obligation, so a
// form added without regenerating the codec does not build.
type Value interface {
	encoding.TextMarshaler
	value()
}

// Adapt is an explicit adaption: Value carried at the type To — the node every
// implicit conversion the checker accepted becomes, so nothing converts
// silently in the IR. The post-check write-back wraps a value at
// each position whose expected type differs from the value's own: a default
// integer literal settling into a sized one (Adapt to short), a value adapting
// to a nominal type (Adapt to Level), and a value flowing into a union (Adapt
// to the union — the inner value's type is the member it tags, the
// ir.Constant.UnionTag the folder computes by the same member selection). The
// adaptions nest where they compose: a literal into short | error settles to
// short inside and tags the union outside. To is also the node's settled type.
type Adapt struct {
	Value Value
	To    Type
}

func (*Adapt) value() {}

// IntLiteral is an integer literal. Its Text is the literal as written; the
// evaluated value lives on Const.Eval. Syntax is the literal this lowered from
// — an editor/position anchor and write-back key only, never semantics.
type IntLiteral struct {
	Text   string
	Type   Type
	Syntax *ast.IntLit `tree:"-"`
}

func (*IntLiteral) value() {}

// StringLiteral is a string literal. Value is the decoded string; the evaluated
// value lives on Const.Eval (the same string).
type StringLiteral struct {
	Value  string
	Type   Type
	Syntax *ast.StringLit `tree:"-"`
}

func (*StringLiteral) value() {}

// BoolLiteral is a boolean literal, true or false.
type BoolLiteral struct {
	Value  bool
	Type   Type
	Syntax *ast.BoolLit `tree:"-"`
}

func (*BoolLiteral) value() {}

// DatetimeLiteral is a datetime literal. Its Text is the literal as written;
// the normalized UTC instant lives on Const.Eval.
type DatetimeLiteral struct {
	Text   string
	Type   Type
	Syntax *ast.DatetimeLit `tree:"-"`
}

func (*DatetimeLiteral) value() {}

// DurationLiteral is a duration literal. Its Text is the literal as written;
// the totalled milliseconds live on Const.Eval.
type DurationLiteral struct {
	Text   string
	Type   Type
	Syntax *ast.DurationLit `tree:"-"`
}

func (*DurationLiteral) value() {}

// CollectionLiteral is a list or map literal. A list's entries each carry only a
// Value; a map's entries each carry a Key and a Value. An empty literal has no
// entries; its kind comes from the constant's type. Syntax is the literal this
// lowered from — the key the settled type is written back through, and the
// editor's position anchor; it carries no semantics.
type CollectionLiteral struct {
	Entries []CollectionEntry
	Type    Type
	Syntax  *ast.CollectionLit `tree:"-"`
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
// order. The evaluated, canonically ordered value lives on Const.Eval. Syntax
// is the literal this lowered from — the settled-type write-back key and the
// editor's position anchor only, never semantics.
type RecordValue struct {
	TypeName string
	Fields   []RecordField
	Type     Type
	Syntax   *ast.RecordLit `tree:"-"`
}

func (*RecordValue) value() {}

// RecordField is one field initializer of a RecordValue: a name and its value.
type RecordField struct {
	Name  string
	Value Value
}

// Reference is a use of another constant, resolved to its declaration. Syntax
// is the referring expression (an identifier, or a namespace member access) —
// the settled-type write-back key and the editor's position anchor only, never
// semantics.
type Reference struct {
	Target *Const `tree:"ref"`
	Type   Type
	Syntax ast.Expr `tree:"-"`
}

func (*Reference) value() {}

// Call is a resolved method call, the form every operator desugars to: the
// receiver, the method name, and the argument values (one for a binary
// operator, none for a unary). Receiver and arguments are themselves resolved
// values, so a Call is the whole operator expression with references bound.
//
// Setter marks the call form a property write lowers to: receiver.name = v
// builds a Call{Receiver, Method: name, Args: [v], Setter: true}, distinguishing
// it from a hand-written method call receiver.name(v) (which the setter name
// space does not reach). Every operator, ordinary method, and index Call leaves
// it false.
//
// Resolved is the overload individual the type checker selected when the
// method name carries several signatures, written back after the checking walk
// (the lowering is type-blind, so a Call is born with it nil; a
// single-signature method needs no selection and stays nil). It is how the IR
// meets its own doctrine — every reference bound to its declaration — for an
// overloaded call: the .ir dump renders the selected signature, and the folder
// prefers it over its value-kind selection rule. Syntax is the call expression
// this lowered from, the key the write-back pairs the checker's selection with.
//
// Subst is the type-variable solution the checker settled for the call — the
// receiver's type arguments (T = nint for a list<nint> receiver, the bound or
// impl interface's parameters included) combined with what the argument
// matching solved (the R of map) — written back after the checking walk like
// Resolved, and nil for a call that pins no variable. It is the input
// monomorphization (or a runtime reify) reads; without it the checker's
// solution would be used for the result type and discarded.
type Call struct {
	Receiver Value
	Method   string
	Args     []Value
	Setter   bool
	Resolved *Method `tree:"ref"`
	Subst    map[string]Type
	Type     Type
	Syntax   *ast.CallExpr `tree:"-"`
}

func (*Call) value() {}

// FuncCall is a resolved call of a top-level function: the function it
// resolves to and the argument values. Target is picked by the type-blind
// lowering — by unique arity among the overload set, else the set's first
// declaration — so for an overloaded name it is a placeholder until the
// checker's selection is written back: Resolved then carries the selected
// individual and Target is corrected to it (see Call.Resolved). A
// single-signature function's Target is exact from lowering and Resolved
// stays nil. Subst is the solved type-parameter substitution of a generic
// call (the T = nint of identity(42)), written back like Call.Subst and nil
// for a non-generic call.
type FuncCall struct {
	Target   *Function `tree:"ref"`
	Args     []Value
	Resolved *Function `tree:"ref"`
	Subst    map[string]Type
	Type     Type
	Syntax   *ast.CallExpr `tree:"-"`
}

func (*FuncCall) value() {}

// StaticCall is a resolved call of a static fn, written Type.name(args) — a
// function scoped to its type, the Type.Name path enum members and associated
// constants take. Like a Call it holds the owning Def and the static fn's Name;
// Resolved carries the overload individual the checker selected when the name
// has several signatures, written back after the checking walk (see
// Call.Resolved), and Subst the solved type-variable substitution (see
// Call.Subst; nil while static fns stay non-generic). The arguments are
// themselves resolved values.
type StaticCall struct {
	Def      *TypeDef `tree:"ref"`
	Name     string
	Args     []Value
	Resolved *Method `tree:"ref"`
	Subst    map[string]Type
	Type     Type
	Syntax   *ast.CallExpr `tree:"-"`
}

func (*StaticCall) value() {}

// Apply is the application of a function value: the callee — a parameter,
// local, or constant bound to a fn value, or a literal applied immediately —
// applied to the argument values. It is the call form left when no
// declaration claims the callee (a method call binds a Method, a FuncCall a
// Function, a Conversion a type): the callee is itself a resolved value, so
// the application carries no name to bind, only the value graph. Syntax is
// the call expression this lowered from — the settled-type write-back key and
// the editor's position anchor only, never semantics.
type Apply struct {
	Callee Value
	Args   []Value
	Type   Type
	Syntax *ast.CallExpr `tree:"-"`
}

func (*Apply) value() {}

// FuncLiteral is a function-literal value: its parameter names and its lowered
// statement body. Type is the checker-solved *Func — annotations, pushed-down
// expectations, and inferred parts combined — so the parameter and result
// types read off the node even though the lowering carries only names. Syntax
// is the literal this lowered from — the settled-type write-back key and the
// editor's position anchor only, never semantics.
type FuncLiteral struct {
	Params []string
	Body   []Stmt
	Type   Type
	Syntax *ast.FuncLit `tree:"-"`
}

func (*FuncLiteral) value() {}

// SelfValue is the method receiver (the self keyword) inside a method body.
// Type is the enclosing type, filled by the write-back from the method's
// owner. Syntax is the self expression this lowered from, or nil for the
// implicit receiver of a bare self-method call.
type SelfValue struct {
	Type   Type
	Syntax *ast.SelfExpr `tree:"-"`
}

func (*SelfValue) value() {}

// ParamRef is a use of a method parameter, by name. Type is the parameter's
// declared type, read off the enclosing signature by the write-back.
type ParamRef struct {
	Name   string
	Type   Type
	Syntax *ast.Identifier `tree:"-"`
}

func (*ParamRef) value() {}

// LocalRef is a use of a let-bound block-local, by name. It is the value form a
// reference to a mutable local takes — distinct from a ParamRef (an immutable
// parameter) and a Reference (a top-level constant) — so the evaluator reads it
// from the body's mutable environment. Type is the binding's settled type, read
// off the introducing Let (or match arm, or for variable) by the write-back.
type LocalRef struct {
	Name   string
	Type   Type
	Syntax *ast.Identifier `tree:"-"`
}

func (*LocalRef) value() {}

// FieldAccess is a record field access — or a getter read, which shares the
// surface form: Receiver.Field. Syntax is the member expression this lowered
// from — the settled-type write-back key and the editor's position anchor
// only, never semantics.
type FieldAccess struct {
	Receiver Value
	Field    string
	Type     Type
	Syntax   *ast.MemberExpr `tree:"-"`
}

func (*FieldAccess) value() {}

// Conversion is a type conversion or constructor T(Args...), as written T(x) —
// the form a builtin type name takes when applied to its arguments. Most
// conversions take one argument (Level(5), error("msg")); a constructor takes
// several (range(start, end)), so the arguments are a slice. Value returns the
// sole argument for the common single-argument form. Type is the target the
// callee names, which is also the node's settled type (a conversion's type is
// the type it names, whatever its arguments) — the one value form born typed.
type Conversion struct {
	Type   Type
	Args   []Value
	Syntax *ast.CallExpr `tree:"-"`
}

// Value returns a single-argument conversion's argument — the common form
// (Level(5), error("msg")) — or nil when the conversion has no arguments or more
// than one. A multi-argument constructor reads Args directly.
func (c *Conversion) Value() Value {
	if len(c.Args) == 1 {
		return c.Args[0]
	}
	return nil
}

func (*Conversion) value() {}

// Await is an await expression: the explicit suspension point that consumes
// the async effect at a call site. It wraps the awaited value and adds
// nothing to its type — Type is the awaited value's, copied by the
// write-back.
type Await struct {
	Value  Value
	Type   Type
	Syntax *ast.AwaitExpr `tree:"-"`
}

func (*Await) value() {}

// Ternary is a resolved conditional value, cond ? then : else: it yields Then
// when Cond holds and Else otherwise. It is the value form of a two-way choice
// (the if statement's expression counterpart); only the taken branch is
// evaluated, so it keeps its own node rather than lowering to a call. Syntax
// is the expression this lowered from — the settled-type write-back key and
// the editor's position anchor only, never semantics.
type Ternary struct {
	Cond   Value
	Then   Value
	Else   Value
	Type   Type
	Syntax *ast.TernaryExpr `tree:"-"`
}

func (*Ternary) value() {}

// RangeLit is a resolved range literal, lo..hi (closed) or lo...hi (half-open).
// It is the value form of the range builtin written in the surface range syntax,
// kept as its own node rather than lowered to a range(...) Conversion because the
// direction (ascending or descending) and the half-open trim depend on the bound
// values: the equivalent range(start, end, step) is settled by the fold, where a
// literal whose bounds fold matches the constructor it equals exactly. HalfOpen
// distinguishes the "..." form (the larger end excluded) from the closed ".."
// form. The evaluated value (a range ConstRange) lives on Const.Eval, as for
// every other value form.
type RangeLit struct {
	Lower    Value
	Upper    Value
	HalfOpen bool
	Type     Type
	Syntax   *ast.RangeExpr `tree:"-"`
}

func (*RangeLit) value() {}

// NullValue is the null literal.
type NullValue struct {
	Type   Type
	Syntax *ast.NullLit `tree:"-"`
}

func (*NullValue) value() {}

// EnumMemberValue is a resolved reference to an enum member, whether written
// qualified (Rarity.Common) or bare (Common, under an enum expectation). Def is
// the enum definition and Index the member's position within it; the name and
// the base value are read from Def.Enum.Members[Index]. The evaluated value
// (an EnumConstant) lives on Const.Eval, as for every other value form.
type EnumMemberValue struct {
	Def   *TypeDef `tree:"ref"`
	Index int
	Type  Type
	// Syntax is the referring expression — a bare member (an identifier) or the
	// qualified Rarity.Common (a member access); an anchor and write-back key
	// only, never semantics.
	Syntax ast.Expr `tree:"-"`
}

func (*EnumMemberValue) value() {}

// TypeOf returns a value node's settled type — the typed value graph's uniform
// reading. It is nil for a node the post-check write-back never settled (a
// broken declaration, or a graph not yet written back) and for a nil value. A
// Conversion's type is its target (the one form born typed); every other form
// reads its Type field. The switch is exhaustive over the sealed Value forms;
// a new form panics here rather than silently reading as untyped.
// one case = one field read, so the length is the case count, not control
// complexity (the Lexer.Next class of exception).
//
//nolint:funlen // a flat exhaustive dispatch over the 25 sealed Value forms:
func TypeOf(v Value) Type {
	switch v := v.(type) {
	case nil:
		return nil
	case *Adapt:
		// An adaption's type is the type it adapts to.
		return v.To
	case *IntLiteral:
		return v.Type
	case *StringLiteral:
		return v.Type
	case *BoolLiteral:
		return v.Type
	case *DatetimeLiteral:
		return v.Type
	case *DurationLiteral:
		return v.Type
	case *CollectionLiteral:
		return v.Type
	case *RecordValue:
		return v.Type
	case *Reference:
		return v.Type
	case *Call:
		return v.Type
	case *FuncCall:
		return v.Type
	case *StaticCall:
		return v.Type
	case *Apply:
		return v.Type
	case *FuncLiteral:
		return v.Type
	case *SelfValue:
		return v.Type
	case *ParamRef:
		return v.Type
	case *LocalRef:
		return v.Type
	case *FieldAccess:
		return v.Type
	case *Conversion:
		return v.Type
	case *Await:
		return v.Type
	case *Ternary:
		return v.Type
	case *RangeLit:
		return v.Type
	case *NullValue:
		return v.Type
	case *EnumMemberValue:
		return v.Type
	case *AssocConstValue:
		return v.Type
	default:
		panic(fmt.Sprintf("ir: unhandled Value kind %T", v))
	}
}

// SyntaxOf returns the expression a value node lowered from — the editor's
// position anchor and the post-check write-back's pairing key, never a carrier
// of semantics. It is nil for a nil value, for a node lowered with no surface
// form (the implicit self receiver of a bare method call, a property write's
// synthetic setter call), and for an Adapt (a write-back construct with no
// surface form of its own; its inner value carries the anchor). The switch is
// exhaustive over the sealed Value forms; a new form panics here rather than
// silently anchoring nowhere.
// one case = one anchor read (the Lexer.Next class of exception).
//
//nolint:funlen // a flat exhaustive dispatch over the 25 sealed Value forms:
func SyntaxOf(v Value) ast.Expr {
	switch v := v.(type) {
	case nil:
		return nil
	case *Adapt:
		return SyntaxOf(v.Value)
	case *IntLiteral:
		return exprOrNil(v.Syntax)
	case *StringLiteral:
		return exprOrNil(v.Syntax)
	case *BoolLiteral:
		return exprOrNil(v.Syntax)
	case *DatetimeLiteral:
		return exprOrNil(v.Syntax)
	case *DurationLiteral:
		return exprOrNil(v.Syntax)
	case *CollectionLiteral:
		return exprOrNil(v.Syntax)
	case *RecordValue:
		return exprOrNil(v.Syntax)
	case *Reference:
		return v.Syntax // already the interface form; nil stays nil
	case *Call:
		return exprOrNil(v.Syntax)
	case *FuncCall:
		return exprOrNil(v.Syntax)
	case *StaticCall:
		return exprOrNil(v.Syntax)
	case *Apply:
		return exprOrNil(v.Syntax)
	case *FuncLiteral:
		return exprOrNil(v.Syntax)
	case *SelfValue:
		return exprOrNil(v.Syntax)
	case *ParamRef:
		return exprOrNil(v.Syntax)
	case *LocalRef:
		return exprOrNil(v.Syntax)
	case *FieldAccess:
		return exprOrNil(v.Syntax)
	case *Conversion:
		return exprOrNil(v.Syntax)
	case *Await:
		return exprOrNil(v.Syntax)
	case *Ternary:
		return exprOrNil(v.Syntax)
	case *RangeLit:
		return exprOrNil(v.Syntax)
	case *NullValue:
		return exprOrNil(v.Syntax)
	case *EnumMemberValue:
		return v.Syntax // already the interface form; nil stays nil
	case *AssocConstValue:
		return exprOrNil(v.Syntax)
	default:
		panic(fmt.Sprintf("ir: unhandled Value kind %T", v))
	}
}

// exprOrNil widens a concrete syntax pointer to ast.Expr, keeping a nil
// pointer nil — the typed-nil-interface guard every SyntaxOf case needs, kept
// in one place so a case written without it cannot reintroduce the bug.
func exprOrNil[E any, P interface {
	*E
	ast.Expr
}](p P) ast.Expr {
	if p == nil {
		return nil
	}
	return p
}

// AssocConstValue is a resolved reference to a type's associated constant,
// written TypeName.Name (int8.Max, Level.Max). Def is the owning type and Index
// the constant's position in Def.Consts; the name, type, and folded value are
// read from Def.Consts[Index]. The evaluated value lives on Const.Eval, as for
// every other value form.
type AssocConstValue struct {
	Def    *TypeDef `tree:"ref"`
	Index  int
	Type   Type
	Syntax *ast.MemberExpr `tree:"-"`
}

func (*AssocConstValue) value() {}
