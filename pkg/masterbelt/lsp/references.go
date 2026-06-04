package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"sort"
)

// References, rename, and prepare-rename are all the reverse of the resolver:
// given the constant under the cursor, find every name that denotes it — its
// declaration and the value references that resolve to it. The resolved IR makes
// that a single pass: a Reference already points at its target *Const.

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
	return occurrence{}, false
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

// references returns the locations of every reference to the symbol at offset,
// across every file of the workspace (including its declaration when
// includeDecl is set).
func references(doc view, offset int, includeDecl bool) []protocol.Location {
	occ, ok := occurrenceAt(doc, offset, doc.Trees())
	if !ok {
		return nil
	}

	var locations []protocol.Location
	for uri, ranges := range programOccurrences(doc, occ.target, includeDecl) {
		for _, r := range ranges {
			locations = append(locations, protocol.Location{URI: uri, Range: r})
		}
	}
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].URI != locations[j].URI {
			return locations[i].URI < locations[j].URI
		}
		a, b := locations[i].Range.Start, locations[j].Range.Start
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Character < b.Character
	})
	return locations
}

// rename renames the symbol at offset to newName at its declaration and every
// reference, across every file of the workspace. It returns nil if there is no
// symbol under the cursor or newName is not a valid identifier.
func rename(doc view, offset int, newName string) *protocol.WorkspaceEdit {
	occ, ok := occurrenceAt(doc, offset, doc.Trees())
	if !ok || !isIdentifier(newName) {
		return nil
	}

	changes := map[protocol.DocumentURI][]protocol.TextEdit{}
	for uri, ranges := range programOccurrences(doc, occ.target, true) {
		for _, r := range ranges {
			changes[uri] = append(changes[uri], protocol.TextEdit{Range: r, NewText: newName})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	for _, edits := range changes {
		sort.Slice(edits, func(i, j int) bool {
			a, b := edits[i].Range.Start, edits[j].Range.Start
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return a.Character < b.Character
		})
	}
	return &protocol.WorkspaceEdit{Changes: changes}
}

// documentHighlights returns every occurrence of the symbol under the cursor as
// a highlight: its declaration as a write, each value reference as a read. It is
// occurrencesOf rendered for the in-file "highlight all uses" feature.
func documentHighlights(doc view, offset int) []protocol.DocumentHighlight {
	buf := doc.Buffer()
	trees := doc.Trees()

	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok {
		return nil
	}

	write := protocol.DocumentHighlightKindWrite
	read := protocol.DocumentHighlightKindRead

	var highlights []protocol.DocumentHighlight
	if declTree, ok := trees[occ.target.Syntax.Syntax()]; ok {
		if nameTok, ok := nameToken(declTree); ok {
			highlights = append(highlights, protocol.DocumentHighlight{
				Range: toRange(buf, nameTok.Offset(), nameTok.End()),
				Kind:  &write,
			})
		}
	}
	for _, tok := range occurrencesOf(doc, occ.target, trees, false) {
		highlights = append(highlights, protocol.DocumentHighlight{
			Range: toRange(buf, tok.Offset(), tok.End()),
			Kind:  &read,
		})
	}
	return highlights
}

// prepareRename reports the range of the identifier under the cursor so the
// editor can offer to rename it, pre-filled with the current name.
func prepareRename(doc view, offset int) *protocol.PrepareRenameResult {
	trees := doc.Trees()
	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok {
		return nil
	}
	return &protocol.PrepareRenameResult{
		Range:       toRange(doc.Buffer(), occ.token.Offset(), occ.token.End()),
		Placeholder: occ.target.Name,
	}
}

// isIdentifier reports whether s is a valid masterbelt identifier and not a
// reserved word.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := c >= '0' && c <= '9'
		if (i == 0 && !letter) || !(letter || digit) {
			return false
		}
	}
	return token.Lookup(s) == token.Ident
}
