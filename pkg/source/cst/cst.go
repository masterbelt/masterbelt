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
//	text.go  the text representation contract (MarshalText/UnmarshalText)
package cst

import (
	"encoding"
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Kind classifies an internal Node — a grammatical construct assembled from
// child elements. Leaf elements are Tokens and carry a token.Kind instead, so
// the two kind spaces never overlap.
type Kind int

// The node kinds, one per grammatical construct; see each kind's comment.
const (
	File          Kind = iota // the whole source: a sequence of declarations and trailing trivia
	ConstDecl                 // [doc] [pub] const Name [TypeClause] [Initializer]
	TypeClause                // ": TypeExpr"
	Initializer               // "= Expr"
	NameRef                   // an identifier used as a value
	Literal                   // a literal value: an integer, a string, a datetime, a duration, a boolean (true/false), or null
	BinaryExpr                // a binary operation: Expr Op Expr
	RangeExpr                 // a range literal: Expr (".." | "...") Expr
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

	// Interface declarations. An interface is a nominal behaviour: a set of
	// required methods (no body) a type must implement, and provided methods
	// (with a body, the default) it gets for free. A type opts in by writing an
	// interface-tagged impl block at its own definition site. An interface may
	// list one or more parent interfaces after a colon (the supertraits), whose
	// whole contract — required and provided members alike — the child inherits.

	InterfaceDecl    // [doc] [pub] interface Name [GenericParams] [InterfaceParents] "{" InterfaceMember* "}"
	InterfaceParents // ":" TypeName ("," TypeName)*  (the parent interfaces a child inherits from)
	InterfaceMember  // [pub] Name [GenericParams] ParamList ":" TypeExpr [Block]  (required when no Block, provided otherwise)

	// Implementations and method bodies. An impl item is a MethodDecl or a
	// ConstDecl (an associated constant, read as TypeName.Name); the latter
	// reuses the same node a top-level constant uses.

	ImplBlock    // impl [TypeName] "{" (MethodDecl | ConstDecl)* "}"  (the optional TypeName tags the interface this block implements)
	MethodDecl   // [pub] ( Modifier | [extern] [fn] ) Effect* Ident ParamList ":" TypeExpr [Block]
	Modifier     // a context-keyword accessor/static marker on a method: get, set, or static (an Ident the parser recognizes by position)
	ParamList    // "(" [Param ("," Param)*] ")"
	Param        // Ident ":" TypeExpr
	Block        // "{" Stmt* "}"
	ReturnStmt   // return Expr
	LetStmt      // let Ident [TypeClause] "=" Expr  (a mutable block-local binding)
	AssignStmt   // Target "=" Expr  (a reassignment statement; Target is a value expression)
	SwitchStmt   // switch Expr "{" ( SwitchArm ( ("," | NL) SwitchArm )* )? "}"
	SwitchArm    // ( Expr ( "," Expr )* | "_" ) "->" ( Stmt | Block )
	IfStmt       // if Expr Block [ else ( IfStmt | Block ) ]
	MatchStmt    // match Expr "{" ( MatchArm ( ("," | NL) MatchArm )* )? "}"
	MatchArm     // MatchPattern "->" ( Stmt | Block )
	MatchPattern // ( PrimaryType [Ident] ) | "_"  (a member type with an optional binding, or the wildcard)
	ForStmt      // for Ident ( "of" | "in" ) Expr Block  (a collection-iteration statement)
	AssertStmt   // assert Expr  (a statement-form assertion, evaluated where it stands — unlike the top-level AssertDecl)

	// Top-level functions.

	FuncDecl // [doc] [pub] fn Ident ParamList ":" TypeExpr ( Block | "->" Expr )

	// Cross-file imports.

	UseDecl // [pub] use ( Ident | UseList | "*" ) from String
	UseList // "{" Ident ("," Ident)* "}"  (the selective-import list)

	// Compile-time assertions.

	AssertDecl // [doc] assert Expr

	// Master declarations. A master declares a master-data table: the record
	// type of its rows and the primary key naming the columns that identify a
	// row. master/record/primary are context keywords — ordinary identifiers the
	// lexer leaves plain, each wrapped in a MasterKeyword node where it is
	// recognized (the Modifier precedent for get/set/static).

	MasterDecl     // [doc] [pub] master Ident "{" ( MasterRecord | MasterPrimary | MasterSource )* "}"
	MasterRecord   // record TypeExpr [WhereClause] [ImplBlock]*  (the row type, reusing the type-body grammar)
	MasterPrimary  // primary ( Ident | "(" Ident ("," Ident)* ")" )  (the key column(s))
	MasterSource   // source "{" SourceEntry* "}"  (where the master's rows are read from)
	SourceEntry    // Ident StringLit [RecordLit]  (a format name, a locator string, and optional format options)
	MasterValidate // validate "{" ValidateClause* "}"  (the per-row and per-table data checks)
	ValidateClause // ( each | all ) Block  (one check block; the keyword names its scope — each is per-row, all per-table)
	MasterKeyword  // a context keyword in a master declaration: master, record, primary, source, validate, each, or all (an Ident the parser recognizes by position)

	Error // a run of tokens that did not fit the grammar

	numKinds // sentinel: the count of Kind values; not a real kind
)

// kindNames maps each Kind to its name, indexed by Kind value.
var kindNames = [...]string{
	File:             "File",
	ConstDecl:        "ConstDecl",
	TypeClause:       "TypeClause",
	Initializer:      "Initializer",
	NameRef:          "NameRef",
	Literal:          "Literal",
	BinaryExpr:       "BinaryExpr",
	RangeExpr:        "RangeExpr",
	TernaryExpr:      "TernaryExpr",
	UnaryExpr:        "UnaryExpr",
	CallExpr:         "CallExpr",
	MemberExpr:       "MemberExpr",
	IndexExpr:        "IndexExpr",
	SelfExpr:         "SelfExpr",
	CollectionLit:    "CollectionLit",
	MapEntry:         "MapEntry",
	RecordLit:        "RecordLit",
	RecordField:      "RecordField",
	FuncLit:          "FuncLit",
	ParenExpr:        "ParenExpr",
	AwaitExpr:        "AwaitExpr",
	TypeDecl:         "TypeDecl",
	GenericParams:    "GenericParams",
	GenericParam:     "GenericParam",
	GenericArgs:      "GenericArgs",
	TypeName:         "TypeName",
	UnionType:        "UnionType",
	RecordType:       "RecordType",
	Field:            "Field",
	FuncType:         "FuncType",
	BuiltinType:      "BuiltinType",
	WhereClause:      "WhereClause",
	EnumDecl:         "EnumDecl",
	EnumMember:       "EnumMember",
	InterfaceDecl:    "InterfaceDecl",
	InterfaceParents: "InterfaceParents",
	InterfaceMember:  "InterfaceMember",
	ImplBlock:        "ImplBlock",
	MethodDecl:       "MethodDecl",
	Modifier:         "Modifier",
	ParamList:        "ParamList",
	Param:            "Param",
	Block:            "Block",
	ReturnStmt:       "ReturnStmt",
	LetStmt:          "LetStmt",
	AssignStmt:       "AssignStmt",
	SwitchStmt:       "SwitchStmt",
	SwitchArm:        "SwitchArm",
	IfStmt:           "IfStmt",
	MatchStmt:        "MatchStmt",
	MatchArm:         "MatchArm",
	MatchPattern:     "MatchPattern",
	ForStmt:          "ForStmt",
	AssertStmt:       "AssertStmt",
	FuncDecl:         "FuncDecl",
	UseDecl:          "UseDecl",
	UseList:          "UseList",
	AssertDecl:       "AssertDecl",
	MasterDecl:       "MasterDecl",
	MasterRecord:     "MasterRecord",
	MasterPrimary:    "MasterPrimary",
	MasterSource:     "MasterSource",
	SourceEntry:      "SourceEntry",
	MasterValidate:   "MasterValidate",
	ValidateClause:   "ValidateClause",
	MasterKeyword:    "MasterKeyword",
	Error:            "Error",
}

// String returns the name of the kind, for snapshots and debugging.
func (k Kind) String() string {
	if 0 <= int(k) && int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Green is an immutable, position-independent CST element: either an internal
// *Node or a leaf *Token. It carries no absolute offset, so the same element
// can stand anywhere and be reused after an edit shifts it. Resolve absolute
// positions by wrapping a Green in a Tree.
//
// Every element marshals to the text representation (see text.go); embedding
// encoding.TextMarshaler in the sealed interface makes that a compile-time
// obligation on each implementation.
//
// The interface is sealed (green is unexported): the only implementations are
// *Node and *Token.
type Green interface {
	encoding.TextMarshaler
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

// Token is a leaf green element wrapping one lexical token: its Kind and the
// source text it covers, but no absolute offset. Storing the text (it is as
// position-independent as the width it determines) makes the green tree
// self-contained — the source is the concatenation of the leaf texts, which is
// what lets the text representation carry the whole tree without a buffer.
type Token struct {
	kind token.Kind
	text string
}

// NewToken builds a leaf token element covering text.
func NewToken(kind token.Kind, text string) *Token {
	return &Token{kind: kind, text: text}
}

// Kind reports the token's lexical category.
func (t *Token) Kind() token.Kind { return t.kind }

// Width reports the token's byte length.
func (t *Token) Width() int { return len(t.text) }

// Text returns the source text the token covers.
func (t *Token) Text() string { return t.text }

func (t *Token) green() {}
