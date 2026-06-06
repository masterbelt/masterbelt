package ast

import "github.com/masterbelt/masterbelt/pkg/source/cst"

// File is a whole source file: its use, constant, type, function, and assert
// declarations in source order. Trivia and any unparsable regions present in
// the CST are dropped here.
type File struct {
	Uses       []*UseDecl
	Decls      []*ConstDecl
	Types      []*TypeDecl
	Enums      []*EnumDecl
	Interfaces []*InterfaceDecl
	Funcs      []*FuncDecl
	Asserts    []*AssertDecl
	syntax     *cst.Node
}

func (f *File) Syntax() *cst.Node { return f.syntax }
func (f *File) node()             {}

// NewFile builds a File node. The constructors keep each node's syntax backlink
// unexported while package parser/abstract populates it.
func NewFile(uses []*UseDecl, decls []*ConstDecl, types []*TypeDecl, enums []*EnumDecl, interfaces []*InterfaceDecl, funcs []*FuncDecl, asserts []*AssertDecl, syntax *cst.Node) *File {
	return &File{Uses: uses, Decls: decls, Types: types, Enums: enums, Interfaces: interfaces, Funcs: funcs, Asserts: asserts, syntax: syntax}
}

// UseDecl is a cross-file import: an optional pub modifier (re-export the
// imported names from this file), a target — exactly one of Namespace
// (use geo from ...), Names (use { a, b } from ...), or Star (use * from ...)
// — and the Path of the file imported from, relative to the importing file.
// Malformed parts are zero values, as elsewhere.
type UseDecl struct {
	Public    bool     // whether the import is re-exported (pub use)
	Namespace string   // the namespace name, or "" for the other targets
	Names     []string // the selective-import names, or nil
	Star      bool     // whether the target is the wildcard "*"
	Path      string   // the imported file's path, decoded, or "" if missing
	syntax    *cst.Node
}

func (d *UseDecl) Syntax() *cst.Node { return d.syntax }
func (d *UseDecl) node()             {}

// NewUseDecl builds a UseDecl node.
func NewUseDecl(public bool, namespace string, names []string, star bool, path string, syntax *cst.Node) *UseDecl {
	return &UseDecl{Public: public, Namespace: namespace, Names: names, Star: star, Path: path, syntax: syntax}
}

// ConstDecl is a constant declaration: an optional run of doc-comment lines, an
// optional pub modifier, the declared Name, an optional Type annotation, and an
// optional initializer Value. The optional parts are nil/zero when the source
// omitted them (or when it was malformed and the parser recovered).
//
// The Type annotation is a full type expression (the same grammar a type
// declaration uses), so a constant may be annotated with a generic type such as
// list<int>.
//
// The same node is reused for an associated constant — a constant scoped to a
// type, declared inside an impl block and read as TypeName.Name (a type
// declaration's Consts and an enum's Consts). Such a constant may carry the
// Builtin marker (`const Max = builtin`): its value comes from the builtin
// registry rather than from an initializer, mirroring a primitive type's
// `= builtin` body. Builtin is never set for a top-level constant.
type ConstDecl struct {
	Doc     []string // doc-comment lines ("///"), stripped of the marker
	Public  bool     // whether the declaration is marked pub
	Name    string   // the declared identifier, or "" if missing
	Type    TypeExpr // the type annotation, or nil if inferred/missing
	Value   Expr     // the initializer expression, or nil if missing/builtin
	Builtin bool     // an associated constant whose value comes from the registry
	syntax  *cst.Node
}

func (d *ConstDecl) Syntax() *cst.Node { return d.syntax }
func (d *ConstDecl) node()             {}

// NewConstDecl builds a ConstDecl node.
func NewConstDecl(doc []string, public bool, name string, typ TypeExpr, value Expr, syntax *cst.Node) *ConstDecl {
	return &ConstDecl{Doc: doc, Public: public, Name: name, Type: typ, Value: value, syntax: syntax}
}

// NewAssocConstDecl builds an associated-constant ConstDecl, including the
// Builtin marker for a `= builtin` body (whose Value is nil).
func NewAssocConstDecl(doc []string, public bool, name string, typ TypeExpr, value Expr, builtin bool, syntax *cst.Node) *ConstDecl {
	return &ConstDecl{Doc: doc, Public: public, Name: name, Type: typ, Value: value, Builtin: builtin, syntax: syntax}
}

// FuncDecl is a top-level function declaration: a method without a receiver.
// Its parameters, result type, and statement body reuse the nodes a method
// declaration is built from; an arrow body (-> Expr) is normalized at lowering
// to a single implicit return, exactly as a function literal's is, so every
// later layer sees one body shape. An extern function declares a native a
// target supplies — the root of an effect — and has no body. The effect list
// (io, async, nondet) declares the function's interaction with the world; an
// empty list means pure. TypeParams holds the function's generic parameters
// (the T in fn f<T: foldable<int>>(...)), each with an optional interface bound;
// a bound names the methods the parameter's type may call in the body, while an
// unbounded parameter is pass-through only.
type FuncDecl struct {
	Doc        []string     // doc-comment lines ("///"), stripped of the marker
	Public     bool         // whether the declaration is marked pub
	Extern     bool         // whether the declaration is marked extern (no body)
	Effects    []string     // the declared effects in source order, or nil for pure
	Name       string       // the declared identifier, or "" if missing
	TypeParams []*TypeParam // the generic type parameters (the T in f<T: I>), or nil
	Params     []*ParamDef  // the parameters, each with its required annotation
	Result     TypeExpr     // the declared result type, or nil if missing
	Body       []Stmt       // the statement body (an arrow body is one return)
	syntax     *cst.Node
}

func (d *FuncDecl) Syntax() *cst.Node { return d.syntax }
func (d *FuncDecl) node()             {}

// NewFuncDecl builds a FuncDecl node.
func NewFuncDecl(doc []string, public, extern bool, effects []string, name string, typeParams []*TypeParam, params []*ParamDef, result TypeExpr, body []Stmt, syntax *cst.Node) *FuncDecl {
	return &FuncDecl{Doc: doc, Public: public, Extern: extern, Effects: effects, Name: name, TypeParams: typeParams, Params: params, Result: result, Body: body, syntax: syntax}
}

// InterfaceDecl is an interface declaration: a nominal behaviour a type opts
// into at its definition site (impl <interface>). It carries an optional run of
// doc-comment lines, an optional pub modifier, the declared Name, its generic
// Params, its Parents — the supertraits whose whole contract the child inherits
// — and its Members — the required methods (no body, which an implementor must
// supply) and the provided methods (with a body, the default an implementor
// gets for free). Required and provided members are distinguished by whether
// their Body is nil.
type InterfaceDecl struct {
	Doc     []string
	Public  bool
	Name    string       // the declared identifier, or "" if missing
	Params  []*TypeParam // generic parameters, in declaration order
	Parents []TypeExpr   // the parent interfaces (supertraits), in declaration order
	Members []*InterfaceMember
	syntax  *cst.Node
}

func (d *InterfaceDecl) Syntax() *cst.Node { return d.syntax }
func (d *InterfaceDecl) node()             {}

// NewInterfaceDecl builds an InterfaceDecl node.
func NewInterfaceDecl(doc []string, public bool, name string, params []*TypeParam, parents []TypeExpr, members []*InterfaceMember, syntax *cst.Node) *InterfaceDecl {
	return &InterfaceDecl{Doc: doc, Public: public, Name: name, Params: params, Parents: parents, Members: members, syntax: syntax}
}

// InterfaceMember is one member of an interface: a method signature and, for a
// provided method, the default Body computed on top of the required methods. A
// required method has Body nil; a provided method carries a Body the implementor
// inherits unless it declares the method directly. TypeParams holds the
// member's own explicit type variables (the A in fold<A>).
type InterfaceMember struct {
	Doc        []string
	Public     bool
	Name       string
	TypeParams []*TypeParam // explicit method type variables (the A in fold<A>), or nil
	Params     []*ParamDef
	Result     TypeExpr
	Body       []Stmt // the provided default body, or nil for a required method
	syntax     *cst.Node
}

func (m *InterfaceMember) Syntax() *cst.Node { return m.syntax }
func (m *InterfaceMember) node()             {}

// Provided reports whether the member is a provided method (it carries a default
// body); a required method has no body.
func (m *InterfaceMember) Provided() bool { return m.Body != nil }

// NewInterfaceMember builds an InterfaceMember node.
func NewInterfaceMember(doc []string, public bool, name string, typeParams []*TypeParam, params []*ParamDef, result TypeExpr, body []Stmt, syntax *cst.Node) *InterfaceMember {
	return &InterfaceMember{Doc: doc, Public: public, Name: name, TypeParams: typeParams, Params: params, Result: result, Body: body, syntax: syntax}
}

// AssertDecl is a compile-time assertion: an optional run of doc-comment lines
// and the asserted Cond expression. An assertion declares no name and has no
// visibility; it exists purely to be checked during analysis. Cond is nil when
// the source omitted the expression (a recovered "assert").
type AssertDecl struct {
	Doc    []string // doc-comment lines ("///"), stripped of the marker
	Cond   Expr     // the asserted expression, or nil if missing
	syntax *cst.Node
}

func (d *AssertDecl) Syntax() *cst.Node { return d.syntax }
func (d *AssertDecl) node()             {}

// NewAssertDecl builds an AssertDecl node.
func NewAssertDecl(doc []string, cond Expr, syntax *cst.Node) *AssertDecl {
	return &AssertDecl{Doc: doc, Cond: cond, syntax: syntax}
}
