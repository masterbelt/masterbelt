package ir

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// Type is a masterbelt type. It is a sealed interface — the variants in this
// file are the only implementations — and it carries no native semantics of its
// own: the primitive types (int8, bool, null, ...) are Builtins whose value
// range and operator implementations are supplied by package builtin, keyed on
// the builtin's name, rather than baked into this representation. That is what
// lets a new primitive be injected by the prelude and the registry without the
// type system hardcoding anything.
//
// String renders a stable, human-readable form used by diagnostics, hovers, and
// the IR dump.
type Type interface {
	typ()
	String() string
}

// --- primitive and invalid types --------------------------------------------

// Builtin is a primitive type, identified by name. Whether it is an integer, its
// value range, and its operator methods are provided by the builtin registry
// keyed on Name — this struct holds only the name. An un-annotated integer
// literal has type Builtin{"int"} (the arbitrary-precision integer that adapts
// to any sized integer) and a boolean literal has type Builtin{"bool"}; there is
// no separate "untyped" kind.
type Builtin struct{ Name string }

func (*Builtin) typ()             {}
func (b *Builtin) String() string { return b.Name }

// invalid is the type of an expression whose type could not be determined. It is
// interned as the Invalid singleton.
type invalid struct{}

func (*invalid) typ()           {}
func (*invalid) String() string { return "invalid" }

// Invalid is the singleton invalid type; it has no fields, so a single shared
// value suffices and it can be compared with ==.
var Invalid Type = &invalid{}

// HasInvalid reports whether t is — or contains, anywhere in a composite —
// the invalid type: some part of it never resolved. Callers use it to keep a
// poisoned type from flowing on (the checker) or from being rendered (the
// editor's hints).
func HasInvalid(t Type) bool {
	switch t := t.(type) {
	case *App:
		for _, a := range t.Args {
			if HasInvalid(a) {
				return true
			}
		}
	case *Func:
		for _, p := range t.Params {
			if HasInvalid(p) {
				return true
			}
		}
		return HasInvalid(t.Result)
	case *Union:
		for _, m := range t.Members {
			if HasInvalid(m) {
				return true
			}
		}
	case *Record:
		for _, f := range t.Fields {
			if HasInvalid(f.Type) {
				return true
			}
		}
	default:
		return t == Invalid
	}
	return false
}

// --- declared and composite types -------------------------------------------

// Named is a reference to a declared type (Coin, Level, ...): a resolved pointer
// to its definition, mirroring how Reference points at a *Const.
type Named struct{ Def *TypeDef }

func (*Named) typ() {}
func (n *Named) String() string {
	if n.Def == nil {
		return "<unresolved type>"
	}
	return n.Def.Name
}

// Union is a sum of member types: A | B | ...
type Union struct{ Members []Type }

func (*Union) typ() {}
func (u *Union) String() string {
	parts := make([]string, len(u.Members))
	for i, m := range u.Members {
		parts[i] = typeString(m)
	}
	return strings.Join(parts, " | ")
}

// Record is an anonymous product type: a sequence of named fields.
type Record struct{ Fields []Field }

// Field is one record field: a name and its type.
type Field struct {
	Name string
	Type Type
}

func (*Record) typ() {}
func (r *Record) String() string {
	parts := make([]string, len(r.Fields))
	for i, f := range r.Fields {
		parts[i] = f.Name + ": " + typeString(f.Type)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// Func is a function type: fn(Params): Result.
type Func struct {
	Params []Type
	Result Type
}

func (*Func) typ() {}
func (f *Func) String() string {
	parts := make([]string, len(f.Params))
	for i, p := range f.Params {
		parts[i] = typeString(p)
	}
	return "fn(" + strings.Join(parts, ", ") + "): " + typeString(f.Result)
}

// TypeVar is a generic type parameter in scope, with an optional constraint.
type TypeVar struct {
	Name  string
	Bound Type // the constraint, or nil if unconstrained
}

func (*TypeVar) typ()             {}
func (v *TypeVar) String() string { return v.Name }

// App is the application of a generic type constructor: Def<Args...>, e.g.
// Optional<int8>.
type App struct {
	Def  *TypeDef
	Args []Type
}

func (*App) typ() {}
func (a *App) String() string {
	name := "<unresolved type>"
	if a.Def != nil {
		name = a.Def.Name
	}
	parts := make([]string, len(a.Args))
	for i, arg := range a.Args {
		parts[i] = typeString(arg)
	}
	return name + "<" + strings.Join(parts, ", ") + ">"
}

// SelfType is the receiver type inside a method signature or body; it resolves
// to the enclosing type during method typing.
type SelfType struct{}

func (*SelfType) typ()           {}
func (*SelfType) String() string { return "self" }

// typeString renders t, treating a nil type as "<none>" so the renderers above
// never panic on a partially-resolved type.
func typeString(t Type) string {
	if t == nil {
		return "<none>"
	}
	return t.String()
}

// --- type definitions -------------------------------------------------------

// TypeDef is a declared type: its name, its generic parameters, the type it is
// defined as (Body), and its methods. A primitive is a TypeDef whose Body is a
// Builtin; a nominal type's Body is its underlying type; a union/record/func
// type's Body is the corresponding composite. Named and App point at a TypeDef,
// so type references form a graph just like value references.
type TypeDef struct {
	Name    string
	Public  bool
	Doc     []string
	Params  []*TypeParam // generic parameters, in declaration order
	Body    Type         // the defined type (nil if missing/invalid)
	Methods []*Method
	Builtin bool // declared as `= builtin`: its semantics come from the registry
	// Enum is the enum description when this definition is an enum (`enum Name
	// {...}`), or nil for every other kind of type. An enum is a nominal type
	// whose value set is fixed: it does not derive its base type's operator
	// methods (it carries only the six comparison methods plus its impl), and
	// it is not assignable to or from its base. Body therefore stays nil for an
	// enum — the base lives in Enum.Base — so the type algebra treats it as a
	// leaf, exactly as it treats a primitive.
	Enum *EnumDef
	// Where is the refinement predicate over self, kept in its evaluable AST
	// form so the predicate can fold per constant (self bound to each value).
	// It is set only when the predicate type-checks to a foldable bool; an
	// unusable predicate is reported at the declaration and stays nil, so the
	// per-constant check never fires for it.
	Where      ast.Expr
	Syntax     *ast.TypeDecl // the type declaration this was resolved from, or nil
	EnumSyntax *ast.EnumDecl // the enum declaration this was resolved from, or nil
}

// EnumDef is the description of an enum type: the name of its base type (an
// integer-family primitive or string) and its members in declaration order.
// Each member's value is the resolved base-type constant (a ConstInt for an
// integer base, a ConstString for a string base); the design forbids duplicate
// values, so a member is uniquely identified by either its name or its value.
type EnumDef struct {
	Base    string
	Members []EnumMember
}

// EnumMember is one member of an enum: its name and its resolved base-type
// value (nil when the value could not be determined, e.g. an unfoldable
// initializer).
type EnumMember struct {
	Name  string
	Value *Constant
}

// TypeParam is one generic parameter of a TypeDef: a name and an optional
// constraint bound.
type TypeParam struct {
	Name  string
	Bound Type // the constraint, or nil if unconstrained
}

// Method is one method declared in a type's impl block: its signature and, for a
// non-extern method, the statement body that computes its result. Extern methods
// have no body — their implementation is a native intrinsic in the builtin
// registry. The effect list declares the method's interaction with the world;
// an empty list means pure.
type Method struct {
	Name    string
	Public  bool
	Extern  bool
	Effects []string // the declared effects in source order, or nil for pure
	Doc     []string
	Params  []Param
	Result  Type
	Body    []Stmt // the resolved body, or nil for an extern method
	// Syntax is the declaration this method resolved from, or nil for the
	// registry's bootstrap methods. With overloading a name no longer pairs a
	// declaration with its resolution — and a dropped duplicate shifts the
	// indexes — so the identity link is what the body checker and the editor
	// navigate by.
	Syntax *ast.MethodDecl
}

// Stmt is a statement in a method body. It is a sealed interface; the only
// implementations are Return and ExprStmt.
type Stmt interface {
	stmt()
}

// Return is a return statement: it yields Value (nil if the source omitted it).
type Return struct {
	Value Value
}

func (*Return) stmt() {}

// ExprStmt is an expression evaluated as a statement.
type ExprStmt struct {
	Value Value
}

func (*ExprStmt) stmt() {}

// Param is one method parameter: a name and its type.
type Param struct {
	Name string
	Type Type
}
