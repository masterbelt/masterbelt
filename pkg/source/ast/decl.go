package ast

import "github.com/masterbelt/masterbelt/pkg/source/cst"

// File is a whole source file: its use, constant, and type declarations in
// source order. Trivia and any unparsable regions present in the CST are
// dropped here.
type File struct {
	Uses   []*UseDecl
	Decls  []*ConstDecl
	Types  []*TypeDecl
	syntax *cst.Node
}

func (f *File) Syntax() *cst.Node { return f.syntax }
func (f *File) node()             {}

// NewFile builds a File node. The constructors keep each node's syntax backlink
// unexported while package parser/abstract populates it.
func NewFile(uses []*UseDecl, decls []*ConstDecl, types []*TypeDecl, syntax *cst.Node) *File {
	return &File{Uses: uses, Decls: decls, Types: types, syntax: syntax}
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
type ConstDecl struct {
	Doc    []string // doc-comment lines ("///"), stripped of the marker
	Public bool     // whether the declaration is marked pub
	Name   string   // the declared identifier, or "" if missing
	Type   TypeExpr // the type annotation, or nil if inferred/missing
	Value  Expr     // the initializer expression, or nil if missing
	syntax *cst.Node
}

func (d *ConstDecl) Syntax() *cst.Node { return d.syntax }
func (d *ConstDecl) node()             {}

// NewConstDecl builds a ConstDecl node.
func NewConstDecl(doc []string, public bool, name string, typ TypeExpr, value Expr, syntax *cst.Node) *ConstDecl {
	return &ConstDecl{Doc: doc, Public: public, Name: name, Type: typ, Value: value, syntax: syntax}
}
