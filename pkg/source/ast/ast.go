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
//
// The node set is split across files: this file holds the Node interface, decl.go
// the declaration nodes (File, ConstDecl), types.go the type-expression nodes,
// and expr.go the expression nodes (Expr and its variants). text_gen.go — the
// generated exact text representation (format v2) — renders a File as a
// diffable snapshot and parses it back.
package ast

//go:generate go run github.com/masterbelt/masterbelt/pkg/source/internal/treegen -marshal Node -roots File -out text_gen.go

import (
	"encoding"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// Node is any AST node. The interface is sealed: the node set is closed to the
// types declared in this package.
//
// Every node marshals to the exact text representation (text_gen.go);
// embedding encoding.TextMarshaler makes that a compile-time obligation, so a
// node form added without regenerating the codec does not build.
type Node interface {
	encoding.TextMarshaler
	// Syntax returns the green CST node this was lowered from.
	Syntax() *cst.Node
	node()
}
