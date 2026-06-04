package ast

import "github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"

// File is a whole source file: its constant and type declarations in source
// order. Trivia and any unparsable regions present in the CST are dropped here.
type File struct {
	Decls  []*ConstDecl
	Types  []*TypeDecl
	syntax *cst.Node
}

func (f *File) Syntax() *cst.Node { return f.syntax }
func (f *File) node()             {}

// NewFile builds a File node. The constructors keep each node's syntax backlink
// unexported while package parser/abstract populates it.
func NewFile(decls []*ConstDecl, types []*TypeDecl, syntax *cst.Node) *File {
	return &File{Decls: decls, Types: types, syntax: syntax}
}

// ConstDecl is a constant declaration: an optional run of doc-comment lines, an
// optional pub modifier, the declared Name, an optional Type annotation, and an
// optional initializer Value. The optional parts are nil/zero when the source
// omitted them (or when it was malformed and the parser recovered).
type ConstDecl struct {
	Doc    []string // doc-comment lines ("///"), stripped of the marker
	Public bool     // whether the declaration is marked pub
	Name   string   // the declared identifier, or "" if missing
	Type   *TypeRef // the type annotation, or nil if inferred/missing
	Value  Expr     // the initializer expression, or nil if missing
	syntax *cst.Node
}

func (d *ConstDecl) Syntax() *cst.Node { return d.syntax }
func (d *ConstDecl) node()             {}

// NewConstDecl builds a ConstDecl node.
func NewConstDecl(doc []string, public bool, name string, typ *TypeRef, value Expr, syntax *cst.Node) *ConstDecl {
	return &ConstDecl{Doc: doc, Public: public, Name: name, Type: typ, Value: value, syntax: syntax}
}

// TypeRef is a reference to a type by name (the only form of type so far).
type TypeRef struct {
	Name   string
	syntax *cst.Node
}

func (t *TypeRef) Syntax() *cst.Node { return t.syntax }
func (t *TypeRef) node()             {}

// NewTypeRef builds a TypeRef node.
func NewTypeRef(name string, syntax *cst.Node) *TypeRef {
	return &TypeRef{Name: name, syntax: syntax}
}
