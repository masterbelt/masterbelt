package ast

import "github.com/masterbelt/masterbelt/pkg/source/cst"

// This file holds the type-declaration side of the AST: the TypeDecl node, the
// type-expression nodes (TypeExpr and its variants), and the impl members
// (methods, their parameters, and statement bodies). The expression nodes a
// method body is built from live in expr.go.

// --- type declarations ------------------------------------------------------

// TypeDecl is a type declaration: an optional run of doc-comment lines, an
// optional pub modifier, the declared Name, its generic Params, the type it is
// defined as (Body), and the methods of its impl block.
type TypeDecl struct {
	Doc     []string
	Public  bool
	Name    string       // the declared identifier, or "" if missing
	Params  []*TypeParam // generic parameters, in declaration order
	Body    TypeExpr     // the defined type, or nil if missing
	Methods []*MethodDecl
	syntax  *cst.Node
}

func (d *TypeDecl) Syntax() *cst.Node { return d.syntax }
func (d *TypeDecl) node()             {}

// NewTypeDecl builds a TypeDecl node.
func NewTypeDecl(doc []string, public bool, name string, params []*TypeParam, body TypeExpr, methods []*MethodDecl, syntax *cst.Node) *TypeDecl {
	return &TypeDecl{Doc: doc, Public: public, Name: name, Params: params, Body: body, Methods: methods, syntax: syntax}
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

// MethodDecl is a method of an impl block: its modifiers, name, parameters,
// result type, and body. An extern method has no body (Body is nil); its
// implementation is a native intrinsic.
type MethodDecl struct {
	Doc    []string
	Public bool
	Extern bool
	Name   string
	Params []*ParamDef
	Result TypeExpr
	Body   []Stmt // the statement body, or nil for an extern method
	syntax *cst.Node
}

func (m *MethodDecl) Syntax() *cst.Node { return m.syntax }
func (m *MethodDecl) node()             {}

// NewMethodDecl builds a MethodDecl node.
func NewMethodDecl(doc []string, public, extern bool, name string, params []*ParamDef, result TypeExpr, body []Stmt, syntax *cst.Node) *MethodDecl {
	return &MethodDecl{Doc: doc, Public: public, Extern: extern, Name: name, Params: params, Result: result, Body: body, syntax: syntax}
}

// Stmt is a statement inside a method body: a return (ReturnStmt) or a bare
// expression statement (ExprStmt).
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
