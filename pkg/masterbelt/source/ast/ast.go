// Package ast defines the abstract syntax tree of the masterbelt language: the
// clean, trivia-free shape of a program, lowered from the concrete tree
// (package source/cst) by package parser/abstract.
//
// Where the CST is lossless and width-based, the AST is the opposite: it keeps
// only what the grammar means — names, modifiers, structure — with the source
// text of identifiers and literals already resolved into plain strings. That
// makes AST nodes position-independent (they hold no offsets), which is what
// lets the lowering reuse the node of an unedited declaration verbatim across an
// edit, keyed by the identity of its backing CST node.
//
// Every node keeps a Syntax link back to the green CST node it was lowered from,
// so a consumer can recover source spans and trivia (comments, whitespace) from
// the concrete tree when it needs them. The link is to the position-independent
// green node, so it does not pin the AST node to one location either.
package ast

import "github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"

// Node is any AST node. The interface is sealed: the node set is closed to the
// types declared in this package.
type Node interface {
	// Syntax returns the green CST node this was lowered from.
	Syntax() *cst.Node
	node()
}

// File is a whole source file: the sequence of its declarations in source order.
// Trivia and any unparsable regions present in the CST are dropped here.
type File struct {
	Decls  []*ConstDecl
	syntax *cst.Node
}

func (f *File) Syntax() *cst.Node { return f.syntax }
func (f *File) node()             {}

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

// TypeRef is a reference to a type by name (the only form of type so far).
type TypeRef struct {
	Name   string
	syntax *cst.Node
}

func (t *TypeRef) Syntax() *cst.Node { return t.syntax }
func (t *TypeRef) node()             {}

// Expr is an initializer expression: an IntLit or a NameRef.
type Expr interface {
	Node
	expr()
}

// IntLit is an integer literal. Its Text is the literal as written; no numeric
// parsing or range checking happens at this layer.
type IntLit struct {
	Text   string
	syntax *cst.Node
}

func (l *IntLit) Syntax() *cst.Node { return l.syntax }
func (l *IntLit) node()             {}
func (l *IntLit) expr()             {}

// NameRef is a reference to another declaration by name. Resolving it to its
// target is a job for a later (semantic) layer, not this one.
type NameRef struct {
	Name   string
	syntax *cst.Node
}

func (r *NameRef) Syntax() *cst.Node { return r.syntax }
func (r *NameRef) node()             {}
func (r *NameRef) expr()             {}

// NewFile builds a File node. Constructors live here so the syntax backlink can
// stay unexported while package parser/abstract populates it.
func NewFile(decls []*ConstDecl, syntax *cst.Node) *File {
	return &File{Decls: decls, syntax: syntax}
}

// NewConstDecl builds a ConstDecl node.
func NewConstDecl(doc []string, public bool, name string, typ *TypeRef, value Expr, syntax *cst.Node) *ConstDecl {
	return &ConstDecl{Doc: doc, Public: public, Name: name, Type: typ, Value: value, syntax: syntax}
}

// NewTypeRef builds a TypeRef node.
func NewTypeRef(name string, syntax *cst.Node) *TypeRef {
	return &TypeRef{Name: name, syntax: syntax}
}

// NewIntLit builds an IntLit node.
func NewIntLit(text string, syntax *cst.Node) *IntLit {
	return &IntLit{Text: text, syntax: syntax}
}

// NewNameRef builds a NameRef node.
func NewNameRef(name string, syntax *cst.Node) *NameRef {
	return &NameRef{Name: name, syntax: syntax}
}
