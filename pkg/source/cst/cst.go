// Package cst defines the concrete syntax tree of the masterbelt language: a
// lossless, position-independent tree built directly over the lexer's token
// stream.
//
// The tree is "green" in the Roslyn/rust-analyzer sense. Every element stores
// only its byte Width, never an absolute offset, so an unchanged subtree can be
// reused verbatim after an edit shifts it — exactly the property the lexer
// relies on for tokens, lifted to whole declarations. Splicing a new tree after
// an edit is therefore just list concatenation; absolute positions fall out by
// summing widths from the root, which the positioned Tree does on demand.
//
// The tree is lossless: every token the lexer emits, trivia included
// (whitespace, newlines, comments), appears exactly once as a leaf, so an
// in-order walk of the leaves reproduces the source byte for byte. Trivia
// attaches forward — a run of trivia becomes the leading children of the
// construct that follows it (so doc comments land on their declaration), and
// trivia after the last declaration becomes trailing children of the File.
//
// The package is organised as:
//
//	cst.go   the Kind enum, the Green element interface, and the Node/Token greens
//	tree.go  the positioned Tree view and the Sprint/Equal helpers
package cst

import (
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Kind classifies an internal Node — a grammatical construct assembled from
// child elements. Leaf elements are Tokens and carry a token.Kind instead, so
// the two kind spaces never overlap.
type Kind int

const (
	File          Kind = iota // the whole source: a sequence of declarations and trailing trivia
	ConstDecl                 // [doc] [pub] const Name [TypeClause] [Initializer]
	TypeClause                // ": TypeExpr"
	Initializer               // "= Expr"
	NameRef                   // an identifier used as a value
	Literal                   // a literal value: an integer, a string, a datetime, a duration, a boolean (true/false), or null
	BinaryExpr                // a binary operation: Expr Op Expr
	TernaryExpr               // a conditional value: Expr "?" Expr ":" Expr
	UnaryExpr                 // a prefix operation: Op Expr
	CallExpr                  // a call: Callee "(" [Expr ("," Expr)*] ")"
	MemberExpr                // a member access: Receiver "." Ident
	IndexExpr                 // an index access: Receiver "[" Expr "]"
	SelfExpr                  // the "self" receiver inside a method body
	CollectionLit             // a list or map literal: "[" ( Expr | MapEntry )* "]"
	MapEntry                  // one map-literal entry: Expr ":" Expr
	RecordLit                 // a record literal: [Ident] "{" RecordField* "}"
	RecordField               // one record-literal field initializer: Ident ":" Expr
	FuncLit                   // a function literal: fn ParamList ":" TypeExpr Block
	ParenExpr                 // a parenthesized grouping: "(" Expr ")"
	AwaitExpr                 // an await expression: await Expr (the explicit suspension point)

	// Type declarations and the type-expression grammar.
	TypeDecl      // [doc] [pub] type Name [GenericParams] "=" TypeExpr [WhereClause] [ImplBlock]
	GenericParams // "<" GenericParam ("," GenericParam)* ">"  (declaration side)
	GenericParam  // Ident [":" TypeExpr]  (a type parameter with an optional constraint)
	GenericArgs   // "<" TypeExpr ("," TypeExpr)* ">"  (application side)
	TypeName      // a named type, possibly applied: Ident [GenericArgs] — or the self/null types
	UnionType     // PrimaryType ("|" PrimaryType)+
	RecordType    // "{" Field* "}"
	Field         // Ident ":" TypeExpr
	FuncType      // fn ParamList ":" TypeExpr
	BuiltinType   // builtin [GenericArgs] — a primitive whose semantics come from the registry
	WhereClause   // where Expr — the refinement predicate of a nominal type

	// Enum declarations.
	EnumDecl   // [doc] [pub] enum Name [":" TypeExpr] "{" EnumMember ( ("," | NL) EnumMember )* "}" [ImplBlock]
	EnumMember // Ident [Initializer]  (one named member, with an optional "= ConstExpr" value)

	// Implementations and method bodies. An impl item is a MethodDecl or a
	// ConstDecl (an associated constant, read as TypeName.Name); the latter
	// reuses the same node a top-level constant uses.
	ImplBlock  // impl "{" (MethodDecl | ConstDecl)* "}"
	MethodDecl // [pub] [extern] [fn] Ident ParamList ":" TypeExpr [Block]
	ParamList  // "(" [Param ("," Param)*] ")"
	Param      // Ident ":" TypeExpr
	Block      // "{" Stmt* "}"
	ReturnStmt // return Expr
	LetStmt    // let Ident [TypeClause] "=" Expr  (a mutable block-local binding)
	AssignStmt // Target "=" Expr  (a reassignment statement; Target is a value expression)
	SwitchStmt // switch Expr "{" ( SwitchArm ( ("," | NL) SwitchArm )* )? "}"
	SwitchArm  // ( Expr ( "," Expr )* | "_" ) "->" ( Stmt | Block )
	IfStmt     // if Expr Block [ else ( IfStmt | Block ) ]

	// Top-level functions.
	FuncDecl // [doc] [pub] fn Ident ParamList ":" TypeExpr ( Block | "->" Expr )

	// Cross-file imports.
	UseDecl // [pub] use ( Ident | UseList | "*" ) from String
	UseList // "{" Ident ("," Ident)* "}"  (the selective-import list)

	// Compile-time assertions.
	AssertDecl // [doc] assert Expr

	Error // a run of tokens that did not fit the grammar
)

// kindNames maps each Kind to its name, indexed by Kind value.
var kindNames = [...]string{
	File:          "File",
	ConstDecl:     "ConstDecl",
	TypeClause:    "TypeClause",
	Initializer:   "Initializer",
	NameRef:       "NameRef",
	Literal:       "Literal",
	BinaryExpr:    "BinaryExpr",
	TernaryExpr:   "TernaryExpr",
	UnaryExpr:     "UnaryExpr",
	CallExpr:      "CallExpr",
	MemberExpr:    "MemberExpr",
	IndexExpr:     "IndexExpr",
	SelfExpr:      "SelfExpr",
	CollectionLit: "CollectionLit",
	MapEntry:      "MapEntry",
	RecordLit:     "RecordLit",
	RecordField:   "RecordField",
	FuncLit:       "FuncLit",
	ParenExpr:     "ParenExpr",
	AwaitExpr:     "AwaitExpr",
	TypeDecl:      "TypeDecl",
	GenericParams: "GenericParams",
	GenericParam:  "GenericParam",
	GenericArgs:   "GenericArgs",
	TypeName:      "TypeName",
	UnionType:     "UnionType",
	RecordType:    "RecordType",
	Field:         "Field",
	FuncType:      "FuncType",
	BuiltinType:   "BuiltinType",
	WhereClause:   "WhereClause",
	EnumDecl:      "EnumDecl",
	EnumMember:    "EnumMember",
	ImplBlock:     "ImplBlock",
	MethodDecl:    "MethodDecl",
	ParamList:     "ParamList",
	Param:         "Param",
	Block:         "Block",
	ReturnStmt:    "ReturnStmt",
	LetStmt:       "LetStmt",
	AssignStmt:    "AssignStmt",
	SwitchStmt:    "SwitchStmt",
	SwitchArm:     "SwitchArm",
	IfStmt:        "IfStmt",
	FuncDecl:      "FuncDecl",
	UseDecl:       "UseDecl",
	UseList:       "UseList",
	AssertDecl:    "AssertDecl",
	Error:         "Error",
}

// String returns the name of the kind, for snapshots and debugging.
func (k Kind) String() string {
	if 0 <= int(k) && int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Green is an immutable, position-independent CST element: either an internal
// *Node or a leaf *Token. It stores only its byte Width, so the same element can
// stand at any absolute offset and be reused after an edit shifts it. Resolve
// absolute positions and source text by wrapping a Green in a Tree.
//
// The interface is sealed (green is unexported): the only implementations are
// *Node and *Token.
type Green interface {
	// Width is the element's length in bytes — for a Node, the sum of its
	// children's widths.
	Width() int
	green()
}

// Node is an internal green element: a Kind and the ordered child elements it is
// built from. Its Width is the sum of the children's widths.
type Node struct {
	kind     Kind
	width    int
	children []Green
}

// NewNode builds a Node of the given kind from children, computing its width.
// children is retained, not copied; the caller must not mutate it afterwards.
func NewNode(kind Kind, children []Green) *Node {
	width := 0
	for _, c := range children {
		width += c.Width()
	}
	return &Node{kind: kind, width: width, children: children}
}

// Kind reports the node's grammatical category.
func (n *Node) Kind() Kind { return n.kind }

// Width reports the node's byte length.
func (n *Node) Width() int { return n.width }

// Children returns the node's child elements in source order. The result aliases
// the node's storage; do not mutate it.
func (n *Node) Children() []Green { return n.children }

func (n *Node) green() {}

// Token is a leaf green element wrapping one lexical token. Like the lexer's
// token it carries its Kind and Width but no absolute offset; the covered text
// is read from the buffer on demand once a Tree supplies the offset.
type Token struct {
	kind  token.Kind
	width int
}

// NewToken builds a leaf token element.
func NewToken(kind token.Kind, width int) *Token {
	return &Token{kind: kind, width: width}
}

// Kind reports the token's lexical category.
func (t *Token) Kind() token.Kind { return t.kind }

// Width reports the token's byte length.
func (t *Token) Width() int { return t.width }

func (t *Token) green() {}
