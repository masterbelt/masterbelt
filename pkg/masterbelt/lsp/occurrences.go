package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// The reverse of the resolver: given a name under the cursor, the constant or
// type it denotes (occurrenceAt, exprOccurrenceAt), and given a target, every
// name that denotes it in a file (occurrencesOf, typeOccurrencesOf) or across
// the whole workspace (programOccurrences, programTypeOccurrences). The
// resolved IR makes this a single pass: a Reference already points at its
// target.

// occurrence is a name token under the cursor together with the constant it
// denotes (the declaration itself, or a reference's target).
type occurrence struct {
	token  cst.Tree
	target *ir.Const
}

// occurrenceAt finds the declaration name, value-position identifier,
// namespace member access, or selective-import name at offset — including a
// reference nested inside an expression — and the constant it denotes (which
// may be declared in another file of the program).
func occurrenceAt(doc view, offset int, trees map[cst.Green]cst.Tree) (occurrence, bool) {
	// A selective-import name (the cursor on Origin in use { Origin } from ...)
	// denotes the constant it imports.
	buf := doc.Buffer()
	for _, u := range doc.AST().File().Uses {
		t, ok := trees[u.Syntax()]
		if !ok || !within(t, offset) {
			continue
		}
		for _, nameTok := range useNameTokens(t) {
			if !within(nameTok, offset) {
				continue
			}
			if target := doc.ResolveUseName(u, nameTok.Text(buf)); target != nil {
				return occurrence{token: nameTok, target: target}, true
			}
		}
	}

	for _, c := range doc.Module().Consts {
		decl := c.Syntax

		// A value-position identifier in the initializer, at any depth.
		if decl.Value != nil {
			occ, found, sawIdent := exprOccurrenceAt(doc, decl.Value, offset, trees)
			if found {
				return occ, true
			}
			if sawIdent {
				return occurrence{}, false // an undefined reference denotes nothing
			}
		}

		// The declaration's own name.
		if declTree, ok := trees[decl.Syntax()]; ok {
			if nameTok, ok := nameToken(declTree); ok && within(nameTok, offset) {
				return occurrence{token: nameTok, target: c}, true
			}
		}
	}

	// A reference inside an assertion's condition, exactly as in an
	// initializer.
	for _, a := range doc.Module().Asserts {
		if a.Syntax.Cond == nil {
			continue
		}
		if occ, found, _ := exprOccurrenceAt(doc, a.Syntax.Cond, offset, trees); found {
			return occ, true
		}
	}

	// A reference inside a method or function body — a return value, a let
	// initializer, an assignment, or anywhere in a switch/if's control flow —
	// resolves exactly as one in an initializer does, so find-references and
	// rename reach a constant used from a body.
	var occ occurrence
	var found bool
	forEachBodyExpr(doc.AST().File(), func(e ast.Expr) {
		if found {
			return
		}
		if o, hit, _ := exprOccurrenceAt(doc, e, offset, trees); hit {
			occ, found = o, true
		}
	})
	if found {
		return occ, true
	}
	return occurrence{}, false
}

// forEachBodyExpr calls fn for every top-level expression of every method,
// enum-method, interface-default, and function body in the file — the shared
// statement walk over each body, descending into a function literal's own body
// — so the occurrence engines reach a reference living in a body (or in a
// lambda nested in one) the same way they reach those in a const initializer.
func forEachBodyExpr(file *ast.File, fn func(ast.Expr)) {
	var walk func(body []ast.Stmt)
	walk = func(body []ast.Stmt) {
		ast.WalkBodyExprs(body, func(e ast.Expr) {
			fn(e)
			// A function literal is its own scope, so WalkExprs (which the
			// callers run on each yielded expression) stops at its boundary;
			// descend into its body here so references inside a lambda are
			// reached too.
			ast.WalkExprs(e, func(inner ast.Expr) bool {
				if lit, ok := inner.(*ast.FuncLit); ok {
					walk(lit.Body)
				}
				return true
			})
		})
	}
	for _, td := range file.Types {
		for _, m := range td.Methods {
			walk(m.Body)
		}
	}
	for _, ed := range file.Enums {
		for _, m := range ed.Methods {
			walk(m.Body)
		}
	}
	for _, id := range file.Interfaces {
		for _, m := range id.Members {
			walk(m.Body)
		}
	}
	for _, fd := range file.Funcs {
		walk(fd.Body)
	}
}

// exprOccurrenceAt finds the value-position identifier or namespace member
// access at offset within e: the reference and the constant it denotes.
// sawIdent reports that the cursor sat on an identifier even when it denotes
// nothing (an undefined reference), so the caller can stop searching.
func exprOccurrenceAt(doc view, e ast.Expr, offset int, trees map[cst.Green]cst.Tree) (occ occurrence, found, sawIdent bool) {
	var hit *ast.Identifier
	ast.WalkValueIdents(e, func(id *ast.Identifier) {
		if t, ok := trees[id.Syntax()]; ok && within(t, offset) {
			hit = id
		}
	})
	if hit != nil {
		if target := doc.Resolve(hit); target != nil {
			return occurrence{token: trees[hit.Syntax()], target: target}, true, true
		}
		// Fall through: the cursor may sit on a namespace access (its
		// receiver is an identifier that names no value).
	}

	// A namespace member access (geo.Origin): the cursor must sit on the
	// member's own name — the receiver names a namespace, not a value, and
	// denotes nothing by itself.
	var member occurrence
	walkMemberExprs(e, func(m *ast.MemberExpr) {
		t, ok := trees[m.Syntax()]
		if !ok {
			return
		}
		nameTok, ok := memberNameToken(t)
		if !ok || !within(nameTok, offset) {
			return
		}
		if target := doc.ResolveMember(m); target != nil {
			member = occurrence{token: nameTok, target: target}
		}
	})
	if member.target != nil {
		return member, true, hit != nil
	}
	return occurrence{}, false, hit != nil
}

// walkMemberExprs visits every member access in e — a thin filter over the
// shared ast.WalkExprs traversal, so it can never drift from the walks name
// resolution uses.
func walkMemberExprs(e ast.Expr, fn func(*ast.MemberExpr)) {
	ast.WalkExprs(e, func(e ast.Expr) bool {
		if m, ok := e.(*ast.MemberExpr); ok {
			fn(m)
		}
		return true
	})
}

// occurrencesOf returns every token that names target — its declaration name
// (when includeDecl is set), every selective-import name that binds it, and
// every value-position identifier that resolves to it, wherever it sits in an
// expression. This is the reverse of resolution.
func occurrencesOf(doc view, target *ir.Const, trees map[cst.Green]cst.Tree, includeDecl bool) []cst.Tree {
	var tokens []cst.Tree

	if includeDecl {
		if declTree, ok := trees[target.Syntax.Syntax()]; ok {
			if nameTok, ok := nameToken(declTree); ok {
				tokens = append(tokens, nameTok)
			}
		}
	}

	// A selective-import name (use { Origin } from ...) names the constant it
	// binds: a rename must rewrite it too, or it would leave a dangling import.
	buf := doc.Buffer()
	for _, u := range doc.AST().File().Uses {
		t, ok := trees[u.Syntax()]
		if !ok {
			continue
		}
		for _, nameTok := range useNameTokens(t) {
			if doc.ResolveUseName(u, nameTok.Text(buf)) == target {
				tokens = append(tokens, nameTok)
			}
		}
	}

	for _, c := range doc.Module().Consts {
		if c.Syntax.Value == nil {
			continue
		}
		tokens = append(tokens, exprOccurrencesOf(doc, c.Syntax.Value, target, trees)...)
	}
	for _, a := range doc.Module().Asserts {
		if a.Syntax.Cond == nil {
			continue
		}
		tokens = append(tokens, exprOccurrencesOf(doc, a.Syntax.Cond, target, trees)...)
	}
	// Every reference inside a method or function body, so a rename rewrites a
	// constant used from a body too (and find-references reports it) — without
	// this the WorkspaceEdit would silently leave a dangling reference.
	forEachBodyExpr(doc.AST().File(), func(e ast.Expr) {
		tokens = append(tokens, exprOccurrencesOf(doc, e, target, trees)...)
	})
	return tokens
}

// exprOccurrencesOf returns the tokens within e that name target: every
// value-position identifier and namespace member access that resolves to it.
func exprOccurrencesOf(doc view, e ast.Expr, target *ir.Const, trees map[cst.Green]cst.Tree) []cst.Tree {
	var tokens []cst.Tree
	ast.WalkValueIdents(e, func(id *ast.Identifier) {
		if doc.Resolve(id) == target {
			if t, ok := trees[id.Syntax()]; ok {
				tokens = append(tokens, t)
			}
		}
	})
	walkMemberExprs(e, func(m *ast.MemberExpr) {
		if doc.ResolveMember(m) == target {
			if t, ok := trees[m.Syntax()]; ok {
				if nameTok, ok := memberNameToken(t); ok {
					tokens = append(tokens, nameTok)
				}
			}
		}
	})
	return tokens
}

// useNameTokens returns the positioned name tokens of a use declaration's
// selective-import list, in source order — empty for namespace and wildcard
// imports (whose Ident children sit outside a UseList node).
func useNameTokens(use cst.Tree) []cst.Tree {
	var tokens []cst.Tree
	for _, child := range use.Children() {
		if kind, ok := child.Kind(); !ok || kind != cst.UseList {
			continue
		}
		for _, item := range child.Children() {
			if tok, ok := item.Token(); ok && tok.Kind() == token.Ident {
				tokens = append(tokens, item)
			}
		}
	}
	return tokens
}

// memberNameToken returns the positioned identifier naming the member of a
// member-access tree — its last Ident token child (the receiver's identifier
// is nested in a NameRef node).
func memberNameToken(member cst.Tree) (cst.Tree, bool) {
	children := member.Children()
	for i := len(children) - 1; i >= 0; i-- {
		if tok, ok := children[i].Token(); ok && tok.Kind() == token.Ident {
			return children[i], true
		}
	}
	return cst.Tree{}, false
}

// programOccurrences finds every name denoting target across the whole
// workspace — the declaration (when includeDecl is set) and every value
// reference and namespace member access in every file — as per-file location
// lists. occurrencesOf only yields the declaration in the file whose tree
// holds it, so passing includeDecl to each file stays correct.
func programOccurrences(v view, target *ir.Const, includeDecl bool) map[protocol.DocumentURI][]protocol.Range {
	out := map[protocol.DocumentURI][]protocol.Range{}
	for _, id := range v.ws.prog.Files() {
		fv := view{ws: v.ws, id: id, uri: v.ws.uriFor(id)}
		trees := fv.Trees()
		buf := fv.Buffer()
		for _, tok := range occurrencesOf(fv, target, trees, includeDecl) {
			out[fv.uri] = append(out[fv.uri], toRange(buf, tok.Offset(), tok.End()))
		}
	}
	return out
}

// typeOccurrencesOf returns every token in fv that names target: its
// declaration name (when includeDecl is set and fv declares it), every
// selective-import name that binds it, and every type-expression name that
// resolves to it (qualified or not). This is occurrencesOf for types.
func typeOccurrencesOf(fv view, target *ir.TypeDef, trees map[cst.Green]cst.Tree, includeDecl bool) []cst.Tree {
	var tokens []cst.Tree
	buf := fv.Buffer()

	if includeDecl && target.Syntax != nil {
		if declTree, ok := trees[target.Syntax.Syntax()]; ok {
			if nameTok, ok := nameToken(declTree); ok {
				tokens = append(tokens, nameTok)
			}
		}
	}

	// A selective-import name (use { Point } from ...) binds the type: a
	// rename must rewrite it too, or it would leave a dangling import.
	for _, u := range fv.AST().File().Uses {
		t, ok := trees[u.Syntax()]
		if !ok {
			continue
		}
		for _, nameTok := range useNameTokens(t) {
			if fv.ResolveUseType(u, nameTok.Text(buf)) == target {
				tokens = append(tokens, nameTok)
			}
		}
	}

	// Every TypeName in the concrete tree — annotations, type-declaration
	// bodies, signatures — resolving exactly as an annotation does. TypeNames
	// nest (list<Coin>), so the walk continues into a TypeName's children.
	own := fv.TypeNames()
	qualified := fv.QualifiedTypeNames()
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		if k, ok := t.Kind(); ok && k == cst.TypeName {
			var idents []cst.Tree
			for _, c := range t.Children() {
				if kk, isTok := c.TokenKind(); isTok && kk == token.Ident {
					idents = append(idents, c)
				}
			}
			switch len(idents) {
			case 1:
				if findTypeDef(own, idents[0].Text(buf)) == target {
					tokens = append(tokens, idents[0])
				}
			case 2:
				if findTypeDef(qualified[idents[0].Text(buf)], idents[1].Text(buf)) == target {
					tokens = append(tokens, idents[1])
				}
			}
		}
		for _, c := range t.Children() {
			walk(c)
		}
	}
	walk(fv.AST().Concrete().Tree())
	return tokens
}

// programTypeOccurrences is programOccurrences for a type definition.
func programTypeOccurrences(v view, target *ir.TypeDef, includeDecl bool) map[protocol.DocumentURI][]protocol.Range {
	out := map[protocol.DocumentURI][]protocol.Range{}
	for _, id := range v.ws.prog.Files() {
		fv := view{ws: v.ws, id: id, uri: v.ws.uriFor(id)}
		trees := fv.Trees()
		buf := fv.Buffer()
		for _, tok := range typeOccurrencesOf(fv, target, trees, includeDecl) {
			out[fv.uri] = append(out[fv.uri], toRange(buf, tok.Offset(), tok.End()))
		}
	}
	return out
}
