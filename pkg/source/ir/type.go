package ir

import (
	"encoding"
	"slices"
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
// String renders a stable, human-readable form used by diagnostics, hovers,
// and the IR dump. Every form also marshals to the exact text representation
// (text.go) — the embedded encoding.TextMarshaler is the compile-time
// obligation, hand-written for the type algebra because of its unexported
// singleton (Invalid) and its reference edges (Named.Def, App.Def).
type Type interface {
	encoding.TextMarshaler
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
		return slices.ContainsFunc(t.Args, HasInvalid)
	case *Func:
		return slices.ContainsFunc(t.Params, HasInvalid) || HasInvalid(t.Result)
	case *Union:
		return slices.ContainsFunc(t.Members, HasInvalid)
	case *Record:
		return slices.ContainsFunc(t.Fields, func(f Field) bool { return HasInvalid(f.Type) })
	default:
		return t == Invalid
	}
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
	Name string
	// Anchor is the type's stable, position-independent address
	// (belt:module/name); see Function.Anchor. Its methods and associated
	// constants anchor beneath it as Anchor#member. "" for an unnamed type.
	Anchor  string
	Public  bool
	Doc     []string
	Params  []*TypeParam // generic parameters, in declaration order
	Body    Type         // the defined type (nil if missing/invalid)
	Methods []*Method
	// Consts are the type's associated constants — the impl block's `const`
	// items, read as TypeName.Name (the same Type.Name path enum members take).
	// Each carries its resolved type and folded value; the design scopes them to
	// the type, so they are reached only through the type, never as a bare name.
	Consts  []*AssocConst
	Builtin bool // declared as `= builtin`: its semantics come from the registry
	// Interface is the interface description when this definition is an interface
	// (`interface Name {...}`), or nil for every other kind of type. An interface
	// is a nominal behaviour: its required and provided methods are carried in
	// Methods (so a value typed as the interface resolves them through the same
	// path a concrete type's methods take), and Interface records which of them
	// are required, for the nominal-satisfaction check. Body stays nil for an
	// interface — it is a leaf in the type algebra, like a primitive or an enum.
	Interface *InterfaceDef
	// Impls are the interfaces this type opts into: the resolved interface
	// applications (foldable<int, T>) of its interface-tagged impl blocks. It is
	// empty for a type that implements no interface. The conformance check reads
	// it to verify the type supplies every required method; method resolution
	// reads it to surface the interfaces' provided methods on the type.
	Impls []Type
	// Enum is the enum description when this definition is an enum (`enum Name
	// {...}`), or nil for every other kind of type. An enum is a nominal type
	// whose value set is fixed: it does not derive its base type's operator
	// methods (it carries only the six comparison methods plus its impl), and
	// it is not assignable to or from its base. Body therefore stays nil for an
	// enum — the base lives in Enum.Base — so the type algebra treats it as a
	// leaf, exactly as it treats a primitive.
	Enum *EnumDef
	// Master is the master-data description when this definition is a master
	// (`master Name {...}`), or nil for every other kind of type. A master is a
	// nominal type whose values are the rows of a master-data table: like an enum
	// it is a leaf the type algebra does not look through — Body stays nil, so a
	// master is opaque to its row record (not assignable to or from it) — with
	// the row's fields in Master.Fields and its primary-key columns in
	// Master.Primary. The kind = master test is Master != nil — what resolving a
	// field typed by a master into a foreign-key reference reads.
	Master *MasterDef
	// Where is the refinement predicate over self as a resolved value graph —
	// self bound to a SelfValue node, every reference resolved, the typed and
	// adapted IR every fold of the predicate runs (self bound to each value).
	// It is set only when the predicate type-checks to a foldable bool; an
	// unusable predicate is reported at the declaration and stays nil, so the
	// per-constant check never fires for it.
	Where           Value
	Syntax          *ast.TypeDecl      `tree:"-"` // the type declaration this was resolved from, or nil
	EnumSyntax      *ast.EnumDecl      `tree:"-"` // the enum declaration this was resolved from, or nil
	InterfaceSyntax *ast.InterfaceDecl `tree:"-"` // the interface declaration this was resolved from, or nil
	MasterSyntax    *ast.MasterDecl    `tree:"-"` // the master declaration this was resolved from, or nil
}

// MasterDef is the description of a master type: the row's type (the record the
// rows conform to, as written — an inline record, a named record alias, or a
// generic application) and the primary-key column names (in declaration order,
// already de-duplicated). The row lives here rather than on TypeDef.Body so a
// master stays a leaf in the type algebra — opaque to its row record — while
// still exposing the row shape its own methods (and a foreign-key reference to
// it) read; keeping it as a Type (rather than a flattened field list) preserves
// the reference to a named alias for liveness and relinking. A primary-key name
// is one of the row's field names; the semantic layer reports one that is not.
type MasterDef struct {
	Row     Type     // the row record type, as written (nil when absent or invalid)
	Primary []string // the primary-key column names, in declaration order
}

// WhereSyntax returns the surface form of the refinement predicate — the
// declaration's where expression — when the definition carries a usable one
// (Where is its value-graph truth), or nil. It is a rendering channel only:
// the violation diagnostic and the editor's hover quote the predicate as
// written, while every fold runs on the Where graph.
func (t *TypeDef) WhereSyntax() ast.Expr {
	if t.Where == nil || t.Syntax == nil {
		return nil
	}
	return t.Syntax.Where
}

// DeclSyntax returns the surface declaration this definition was resolved from
// — a type, enum, interface, or master declaration — as the ast.Node the editor
// and the linter navigate by, or nil for a definition built outside source (the
// prelude's). Exactly one backpointer is set per kind: a Body-bearing type on
// Syntax, the others on their own field, so the first non-nil wins. This is the
// one place that knows where each kind keeps its declaration, so navigation,
// find-references, and the unused-decl lint all read it through here rather than
// re-deriving the switch.
func (t *TypeDef) DeclSyntax() ast.Node {
	switch {
	case t.Syntax != nil:
		return t.Syntax
	case t.EnumSyntax != nil:
		return t.EnumSyntax
	case t.InterfaceSyntax != nil:
		return t.InterfaceSyntax
	case t.MasterSyntax != nil:
		return t.MasterSyntax
	default:
		return nil
	}
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
	// ValueGraph is the resolved initializer value graph (references bound),
	// kept in memory so reachability and find-references can traverse what a
	// member uses; nil for an auto-numbered or string-default member, which
	// names nothing. tree:"-", like the other retained graphs: the text form
	// renders Value, the folded outcome.
	ValueGraph Value `tree:"-"`
}

// InterfaceDef is the description of an interface type: the names of its
// required methods (the ones an implementor must supply), its provided methods
// (the defaults an implementor inherits), and its parents (the supertraits it
// inherits from). The method signatures and bodies themselves live in the
// owning TypeDef's Methods — required and provided alike — so a value typed as
// the interface resolves them through the same path a concrete type's methods
// take; this struct records only which names are required, which is what the
// conformance check needs.
type InterfaceDef struct {
	Required []string // the names of the required methods, in declaration order
	Provided []string // the names of the provided (default) methods, in declaration order
	// Parents are the supertraits this interface inherits from: the resolved
	// parent-interface applications (a bare comparable is a Named, a generic
	// foldable<nint, T> an App). A child inherits the whole contract of every
	// parent — required and provided members alike. The conformance closure and
	// the bound-implication walk read Parents to reach an ancestor's contract
	// through the child; it is empty for an interface with no parents.
	Parents []Type
}

// TypeParam is one generic parameter of a TypeDef: a name and an optional
// constraint bound.
type TypeParam struct {
	Name  string
	Bound Type // the constraint, or nil if unconstrained
}

// MethodKind classifies a method by how it is accessed: an ordinary instance
// method (value.name(args)), a getter (value.name, read like a field), a setter
// (value.name = v), or a static fn (Type.name(args)). It mirrors ast.MethodKind,
// carried onto the resolved method so the type rules and the evaluator place and
// reach each member through the right name space. The registry's bootstrap
// methods and the prelude's existing methods stay MethodNormal (the zero value),
// so every method that pre-dates accessors is unaffected.
type MethodKind int

// The method kinds, one per accessor modifier; see each kind's comment.
const (
	MethodNormal MethodKind = iota // an instance method, the default
	MethodGetter                   // a getter: read as value.name
	MethodSetter                   // a setter: written value.name = v
	MethodStatic                   // a static fn: called Type.name(...)
)

func (k MethodKind) String() string {
	switch k {
	case MethodGetter:
		return "getter"
	case MethodSetter:
		return "setter"
	case MethodStatic:
		return "static"
	default:
		return "method"
	}
}

// Method is one method declared in a type's impl block: its signature and, for a
// non-extern method, the statement body that computes its result. Extern methods
// have no body — their implementation is a native intrinsic in the builtin
// registry. The effect list declares the method's interaction with the world;
// an empty list means pure. Kind records the accessor/static modifier (or
// MethodNormal for an ordinary method), which decides the name space the member
// lives in.
type Method struct {
	Name string
	// Anchor is the method's stable, position-independent address, the owning
	// type's anchor with the member appended (belt:module/Type#name); see
	// Function.Anchor. "" for a method of an unnamed type or built outside a
	// source declaration (the registry's bootstrap methods).
	Anchor  string
	Public  bool
	Extern  bool
	Kind    MethodKind // the accessor/static modifier, or MethodNormal
	Effects []string   // the declared effects in source order, or nil for pure
	Doc     []string
	Params  []Param
	Result  Type
	Body    []Stmt // the resolved body, or nil for an extern method
	// Owner is the definition this method belongs to — the backpointer
	// mirroring Named.Def, so a resolved method names its owner without a
	// module walk (the text form renders Owner.name(signature)). It is
	// stamped by TypeDef.AttachMethods, the one channel methods join a type
	// through; a method is owned by exactly one definition (an interface's
	// provided methods live on the interface and are surfaced on
	// implementors at lookup, never copied).
	Owner *TypeDef `tree:"ref"`
	// Syntax is the declaration this method resolved from, or nil for the
	// registry's bootstrap methods. With overloading a name no longer pairs a
	// declaration with its resolution — and a dropped duplicate shifts the
	// indexes — so the identity link is what the body checker and the editor
	// navigate by.
	Syntax *ast.MethodDecl `tree:"-"`
}

// AttachMethods appends methods to the definition, stamping each one's Owner
// — the single channel methods join a type through, so the backpointer cannot
// drift from the list.
func (t *TypeDef) AttachMethods(methods ...*Method) {
	for _, m := range methods {
		m.Owner = t
	}
	t.Methods = append(t.Methods, methods...)
}

// AssocConst is one associated constant of a type: a constant scoped to the
// type, declared in its impl block and read as TypeName.Name. It carries its
// resolved Type and folded Value (the value is computed at type resolution,
// exactly as an enum member's is). Builtin marks a `= builtin` constant whose
// value comes from the registry (the integer bounds Max/Min): such a constant
// has no source initializer, and the builtin layer fills in its value and type.
type AssocConst struct {
	Name string
	// Anchor is the associated constant's stable, position-independent address,
	// the owning type's anchor with the member appended (belt:module/Type#name);
	// see Function.Anchor. "" for a constant of an unnamed type.
	Anchor  string
	Public  bool
	Doc     []string
	Type    Type      // the resolved type of the constant's value
	Value   *Constant // the folded value, or nil when it could not be folded
	Builtin bool      // value supplied by the registry (`= builtin`)
	// ValueGraph is the resolved initializer value graph (references bound),
	// kept in memory so reachability and find-references can traverse what the
	// constant uses; nil for a `= builtin` constant, which has no initializer.
	// tree:"-", like the other retained graphs: the text form renders Value.
	ValueGraph Value          `tree:"-"`
	Syntax     *ast.ConstDecl `tree:"-"` // the declaration this was resolved from, or nil
}

// Stmt is a statement in a method body. It is a sealed interface; the only
// implementations are Return, ExprStmt, Let, Assign, Switch, Match, and If.
// Embedding encoding.TextMarshaler obliges every statement form to marshal
// to the exact text representation (text_gen.go) at compile time.
type Stmt interface {
	encoding.TextMarshaler
	stmt()
}

// Return is a return statement: it yields Value (nil if the source omitted it).
type Return struct {
	Value  Value
	Syntax *ast.ReturnStmt `tree:"-"` // the statement this was lowered from, a diagnostic anchor
}

func (*Return) stmt() {}

// ExprStmt is an expression evaluated as a statement.
type ExprStmt struct {
	Value  Value
	Syntax *ast.ExprStmt `tree:"-"`
}

func (*ExprStmt) stmt() {}

// Let is a resolved mutable block-local binding: "let Name = Value". The slot is
// referenced by a LocalRef and updated by an Assign. Type is the binding's
// settled type — the annotation when written, otherwise the value's inferred
// type — carried here because the value graph is otherwise untyped.
type Let struct {
	Name   string
	Type   Type
	Value  Value
	Syntax *ast.LetStmt `tree:"-"`
}

func (*Let) stmt() {}

// Assign is a resolved reassignment of a let local: "Name = Value". Name is the
// target local's name (the parser's target expression, validated to be a let
// local by the semantic layer); Value is the new value.
type Assign struct {
	Name   string
	Value  Value
	Syntax *ast.AssignStmt `tree:"-"`
}

func (*Assign) stmt() {}

// Switch is a resolved value-dispatch statement: it runs the body of the first
// arm whose patterns the Scrutinee equals, or the Else body when none match.
// The wildcard "_" arm has been lifted into Else (nil when the switch had no
// wildcard), so the arms carry only value patterns.
type Switch struct {
	Scrutinee Value
	Arms      []SwitchArm
	Else      []Stmt          // the wildcard body, or nil if the switch had no wildcard
	Syntax    *ast.SwitchStmt `tree:"-"`
}

func (*Switch) stmt() {}

// SwitchArm is one resolved arm of a Switch: the values it matches and the body
// it runs.
type SwitchArm struct {
	Values []Value
	Body   []Stmt
}

// Match is a resolved type-dispatch statement: it runs the body of the first arm
// whose member type the Scrutinee's runtime type is, or the Else body when none
// match. The wildcard "_" arm has been lifted into Else (nil when the match had
// no wildcard), so the arms carry only type patterns.
type Match struct {
	Scrutinee Value
	Arms      []MatchArm
	Else      []Stmt         // the wildcard body, or nil if the match had no wildcard
	Syntax    *ast.MatchStmt `tree:"-"`
}

func (*Match) stmt() {}

// MatchArm is one resolved arm of a Match: the member Type it matches, the
// optional binding Name narrowed to that type inside the arm (empty when the arm
// binds nothing), and the body it runs.
type MatchArm struct {
	Type Type
	Name string // the narrowed binding, or "" when the arm binds nothing
	Body []Stmt
}

// If is a resolved boolean control statement: it runs Then when Cond holds,
// otherwise its else branch. ElseIf (an else-if) and Else (a plain else body)
// are mutually exclusive, and both nil means the if had no else. An if yields no
// value — each branch drives control flow by returning.
type If struct {
	Cond   Value
	Then   []Stmt
	ElseIf *If         // the chained "else if", or nil
	Else   []Stmt      // the "else" body, or nil when there is no plain else
	Syntax *ast.IfStmt `tree:"-"`
}

func (*If) stmt() {}

// For is a resolved collection-iteration statement: it visits every element of
// the foldable Iter in fold order, binding Var (a LocalRef inside Body) to each
// in turn — the element value when Of, the key when Of is false — and running
// Body once per element. VarType is the loop variable's settled type (the value
// type for an of-loop, the key type for an in-loop). A for yields no value; it
// drives control flow, and the body accumulates into a let it reassigns.
type For struct {
	Var     string // the loop variable name
	VarType Type   // the loop variable's settled type
	Of      bool   // true for an of-loop (the value), false for an in-loop (the key)
	Iter    Value
	Body    []Stmt
	Syntax  *ast.ForStmt `tree:"-"`
}

func (*For) stmt() {}

// SyntaxOfStmt returns the statement a lowered Stmt came from — its source
// anchor for a diagnostic, never a carrier of semantics. It is nil for a nil
// statement and for one built with no surface form. The switch is exhaustive
// over the sealed Stmt forms; a new form panics here rather than silently
// anchoring nowhere, the contract SyntaxOf keeps for values.
func SyntaxOfStmt(s Stmt) ast.Stmt {
	switch s := s.(type) {
	case nil:
		return nil
	case *Return:
		return stmtOrNil(s.Syntax)
	case *ExprStmt:
		return stmtOrNil(s.Syntax)
	case *Let:
		return stmtOrNil(s.Syntax)
	case *Assign:
		return stmtOrNil(s.Syntax)
	case *Switch:
		return stmtOrNil(s.Syntax)
	case *Match:
		return stmtOrNil(s.Syntax)
	case *If:
		return stmtOrNil(s.Syntax)
	case *For:
		return stmtOrNil(s.Syntax)
	default:
		panic(unhandledStmt(s))
	}
}

// stmtOrNil widens a concrete statement pointer to ast.Stmt, keeping a nil
// pointer nil — the typed-nil guard SyntaxOfStmt needs, the statement twin of
// exprOrNil, kept in one place so a case written without it cannot reintroduce
// the bug.
func stmtOrNil[S any, P interface {
	*S
	ast.Stmt
}](p P) ast.Stmt {
	if p == nil {
		return nil
	}
	return p
}

// Param is one method parameter: a name and its type.
type Param struct {
	Name string
	Type Type
}
