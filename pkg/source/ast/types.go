package ast

import "github.com/masterbelt/masterbelt/pkg/source/cst"

// This file holds the type-declaration side of the AST: the TypeDecl node, the
// type-expression nodes (TypeExpr and its variants), and the impl members
// (methods, their parameters, and statement bodies). The expression nodes a
// method body is built from live in expr.go.

// --- type declarations ------------------------------------------------------

// TypeDecl is a type declaration: an optional run of doc-comment lines, an
// optional pub modifier, the declared Name, its generic Params, the type it is
// defined as (Body), its refinement predicate (Where), and the methods of its
// impl block.
type TypeDecl struct {
	Doc     []string
	Public  bool
	Name    string       // the declared identifier, or "" if missing
	Params  []*TypeParam // generic parameters, in declaration order
	Body    TypeExpr     // the defined type, or nil if missing
	Where   Expr         // the refinement predicate over self, or nil if none
	Methods []*MethodDecl
	syntax  *cst.Node
}

func (d *TypeDecl) Syntax() *cst.Node { return d.syntax }
func (d *TypeDecl) node()             {}

// NewTypeDecl builds a TypeDecl node.
func NewTypeDecl(doc []string, public bool, name string, params []*TypeParam, body TypeExpr, where Expr, methods []*MethodDecl, syntax *cst.Node) *TypeDecl {
	return &TypeDecl{Doc: doc, Public: public, Name: name, Params: params, Body: body, Where: where, Methods: methods, syntax: syntax}
}

// TypeParam is one generic parameter of a TypeDecl: a name and an optional
// constraint (itself a type expression, which may be a union).
type TypeParam struct {
	Name       string
	Constraint TypeExpr // the constraint bound, or nil if unconstrained
	syntax     *cst.Node
}

func (p *TypeParam) Syntax() *cst.Node { return p.syntax }
func (p *TypeParam) node()             {}

// NewTypeParam builds a TypeParam node.
func NewTypeParam(name string, constraint TypeExpr, syntax *cst.Node) *TypeParam {
	return &TypeParam{Name: name, Constraint: constraint, syntax: syntax}
}

// --- enum declarations ------------------------------------------------------

// EnumDecl is an enum declaration: a fixed set of named members belonging to
// one nominal type. It carries an optional run of doc-comment lines, an
// optional pub modifier, the declared Name, the optional Base type annotation
// (the integer family or string; nil means the default int), its Members in
// declaration order, and the methods of its impl block (the same mechanism a
// type declaration's impl uses).
type EnumDecl struct {
	Doc     []string
	Public  bool
	Name    string   // the declared identifier, or "" if missing
	Base    TypeExpr // the base-type annotation, or nil if omitted (defaults to int)
	Members []*EnumMember
	Methods []*MethodDecl
	syntax  *cst.Node
}

func (d *EnumDecl) Syntax() *cst.Node { return d.syntax }
func (d *EnumDecl) node()             {}

// NewEnumDecl builds an EnumDecl node.
func NewEnumDecl(doc []string, public bool, name string, base TypeExpr, members []*EnumMember, methods []*MethodDecl, syntax *cst.Node) *EnumDecl {
	return &EnumDecl{Doc: doc, Public: public, Name: name, Base: base, Members: members, Methods: methods, syntax: syntax}
}

// EnumMember is one member of an enum: its Name and an optional Value
// expression (a constant expression). Value is nil when the source omitted the
// "= ConstExpr" initializer, in which case the member's value is determined by
// the default rules (auto-numbering for an integer base, the member name for a
// string base).
type EnumMember struct {
	Name   string // the member identifier, or "" if missing
	Value  Expr   // the initializer expression, or nil if omitted
	syntax *cst.Node
}

func (m *EnumMember) Syntax() *cst.Node { return m.syntax }
func (m *EnumMember) node()             {}

// NewEnumMember builds an EnumMember node.
func NewEnumMember(name string, value Expr, syntax *cst.Node) *EnumMember {
	return &EnumMember{Name: name, Value: value, syntax: syntax}
}

// --- type expressions -------------------------------------------------------

// TypeExpr is a type expression: a named type (NamedType, also used for the self
// and null types), a union (UnionType), a record (RecordType), or a function
// type (FuncType).
type TypeExpr interface {
	Node
	typeExpr()
}

// NamedType is a type named by an identifier, with optional generic arguments:
// int8, Coin, Optional<int8>, the type parameter T, or the self/null types. A
// type reached through a namespace import carries its qualifier — geo.Point
// has Namespace "geo" and Name "Point"; a plain name has Namespace "".
type NamedType struct {
	Namespace string     // the namespace qualifier, or "" for a plain name
	Name      string     // the type's own name, or "" if missing (geo.)
	Args      []TypeExpr // generic arguments, empty if none
	syntax    *cst.Node
}

func (t *NamedType) Syntax() *cst.Node { return t.syntax }
func (t *NamedType) node()             {}
func (t *NamedType) typeExpr()         {}

// NewNamedType builds a NamedType node.
func NewNamedType(namespace, name string, args []TypeExpr, syntax *cst.Node) *NamedType {
	return &NamedType{Namespace: namespace, Name: name, Args: args, syntax: syntax}
}

// UnionType is a union of member types: A | B | ...
type UnionType struct {
	Members []TypeExpr
	syntax  *cst.Node
}

func (t *UnionType) Syntax() *cst.Node { return t.syntax }
func (t *UnionType) node()             {}
func (t *UnionType) typeExpr()         {}

// NewUnionType builds a UnionType node.
func NewUnionType(members []TypeExpr, syntax *cst.Node) *UnionType {
	return &UnionType{Members: members, syntax: syntax}
}

// RecordType is an anonymous product type: a sequence of named fields.
type RecordType struct {
	Fields []*FieldDef
	syntax *cst.Node
}

func (t *RecordType) Syntax() *cst.Node { return t.syntax }
func (t *RecordType) node()             {}
func (t *RecordType) typeExpr()         {}

// NewRecordType builds a RecordType node.
func NewRecordType(fields []*FieldDef, syntax *cst.Node) *RecordType {
	return &RecordType{Fields: fields, syntax: syntax}
}

// FuncType is a function type: fn(Params): Result.
type FuncType struct {
	Params []*ParamDef
	Result TypeExpr
	syntax *cst.Node
}

func (t *FuncType) Syntax() *cst.Node { return t.syntax }
func (t *FuncType) node()             {}
func (t *FuncType) typeExpr()         {}

// NewFuncType builds a FuncType node.
func NewFuncType(params []*ParamDef, result TypeExpr, syntax *cst.Node) *FuncType {
	return &FuncType{Params: params, Result: result, syntax: syntax}
}

// BuiltinType is the body of a primitive declaration (`= builtin`): the type's
// representation and operator implementations come from the builtin registry,
// not from this declaration. Args mirrors the declaration's generic parameters
// for a generic builtin (builtin<T>).
type BuiltinType struct {
	Args   []TypeExpr
	syntax *cst.Node
}

func (t *BuiltinType) Syntax() *cst.Node { return t.syntax }
func (t *BuiltinType) node()             {}
func (t *BuiltinType) typeExpr()         {}

// NewBuiltinType builds a BuiltinType node.
func NewBuiltinType(args []TypeExpr, syntax *cst.Node) *BuiltinType {
	return &BuiltinType{Args: args, syntax: syntax}
}

// FieldDef is one record field: a name and its type.
type FieldDef struct {
	Name   string
	Type   TypeExpr
	syntax *cst.Node
}

func (f *FieldDef) Syntax() *cst.Node { return f.syntax }
func (f *FieldDef) node()             {}

// NewFieldDef builds a FieldDef node.
func NewFieldDef(name string, typ TypeExpr, syntax *cst.Node) *FieldDef {
	return &FieldDef{Name: name, Type: typ, syntax: syntax}
}

// ParamDef is one parameter of a function type or method: a name and its type.
// In a function literal the annotation is optional — Type is nil when omitted,
// and the checker fills it in from the expected type.
type ParamDef struct {
	Name   string
	Type   TypeExpr // the declared type, or nil if omitted (function literals only)
	syntax *cst.Node
}

func (p *ParamDef) Syntax() *cst.Node { return p.syntax }
func (p *ParamDef) node()             {}

// NewParamDef builds a ParamDef node.
func NewParamDef(name string, typ TypeExpr, syntax *cst.Node) *ParamDef {
	return &ParamDef{Name: name, Type: typ, syntax: syntax}
}

// --- methods and statements -------------------------------------------------

// MethodDecl is a method of an impl block: its modifiers, effects, name,
// parameters, result type, and body. An extern method has no body (Body is
// nil); its implementation is a native intrinsic. The effect list (io, async,
// nondet) declares the method's interaction with the world; an empty list
// means pure.
type MethodDecl struct {
	Doc     []string
	Public  bool
	Extern  bool
	Effects []string // the declared effects in source order, or nil for pure
	Name    string
	Params  []*ParamDef
	Result  TypeExpr
	Body    []Stmt // the statement body, or nil for an extern method
	syntax  *cst.Node
}

func (m *MethodDecl) Syntax() *cst.Node { return m.syntax }
func (m *MethodDecl) node()             {}

// NewMethodDecl builds a MethodDecl node.
func NewMethodDecl(doc []string, public, extern bool, effects []string, name string, params []*ParamDef, result TypeExpr, body []Stmt, syntax *cst.Node) *MethodDecl {
	return &MethodDecl{Doc: doc, Public: public, Extern: extern, Effects: effects, Name: name, Params: params, Result: result, Body: body, syntax: syntax}
}

// Stmt is a statement inside a method body: a return (ReturnStmt), a switch
// (SwitchStmt), or a bare expression statement (ExprStmt).
type Stmt interface {
	Node
	stmt()
}

// ReturnStmt is a "return Expr" statement. Value is nil if the expression was
// missing.
type ReturnStmt struct {
	Value  Expr
	syntax *cst.Node
}

func (s *ReturnStmt) Syntax() *cst.Node { return s.syntax }
func (s *ReturnStmt) node()             {}
func (s *ReturnStmt) stmt()             {}

// NewReturnStmt builds a ReturnStmt node.
func NewReturnStmt(value Expr, syntax *cst.Node) *ReturnStmt {
	return &ReturnStmt{Value: value, syntax: syntax}
}

// ExprStmt is a bare expression evaluated as a statement.
type ExprStmt struct {
	X      Expr
	syntax *cst.Node
}

func (s *ExprStmt) Syntax() *cst.Node { return s.syntax }
func (s *ExprStmt) node()             {}
func (s *ExprStmt) stmt()             {}

// NewExprStmt builds an ExprStmt node.
func NewExprStmt(x Expr, syntax *cst.Node) *ExprStmt {
	return &ExprStmt{X: x, syntax: syntax}
}

// SwitchStmt is a value-dispatch statement: it runs the body of the first arm
// whose value patterns match the Scrutinee (by equality), or — when no arm
// matches — the wildcard Else body. The wildcard arm ("_") is lifted out of the
// Arms list into Else, so the arms carry only value patterns; Else is nil when
// the switch had no wildcard. Each arm body is its own statement list, so a
// switch nests control flow.
type SwitchStmt struct {
	Scrutinee Expr         // the value branched on (nil if recovered away)
	Arms      []*SwitchArm // the value-pattern arms, in source order
	Else      []Stmt       // the wildcard "_" arm's body, or nil if none
	syntax    *cst.Node
}

func (s *SwitchStmt) Syntax() *cst.Node { return s.syntax }
func (s *SwitchStmt) node()             {}
func (s *SwitchStmt) stmt()             {}

// NewSwitchStmt builds a SwitchStmt node.
func NewSwitchStmt(scrutinee Expr, arms []*SwitchArm, els []Stmt, syntax *cst.Node) *SwitchStmt {
	return &SwitchStmt{Scrutinee: scrutinee, Arms: arms, Else: els, syntax: syntax}
}

// SwitchArm is one non-wildcard arm of a switch: one or more compile-time value
// patterns (Values) and the Body to run when the scrutinee equals any of them.
// A bare enum member in a value position is an ordinary Identifier here; the
// semantic layer resolves it against the scrutinee's enum.
type SwitchArm struct {
	Values []Expr // the value patterns this arm matches, in source order
	Body   []Stmt // the statements to run when the arm matches
	syntax *cst.Node
}

func (a *SwitchArm) Syntax() *cst.Node { return a.syntax }
func (a *SwitchArm) node()             {}

// NewSwitchArm builds a SwitchArm node.
func NewSwitchArm(values []Expr, body []Stmt, syntax *cst.Node) *SwitchArm {
	return &SwitchArm{Values: values, Body: body, syntax: syntax}
}
