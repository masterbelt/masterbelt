package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
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

// occurrenceAt finds the declaration name or value-position identifier at
// offset — including a reference nested inside an expression — and the constant
// it denotes.
func occurrenceAt(doc *semantic.Document, offset int, trees map[cst.Green]cst.Tree) (occurrence, bool) {
	for _, c := range doc.Module().Consts {
		decl := c.Syntax

		// A value-position identifier in the initializer, at any depth.
		if decl.Value != nil {
			var hit *ast.Identifier
			ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
				if t, ok := trees[id.Syntax()]; ok && within(t, offset) {
					hit = id
				}
			})
			if hit != nil {
				if target := doc.Resolve(hit); target != nil {
					return occurrence{token: trees[hit.Syntax()], target: target}, true
				}
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
	return occurrence{}, false
}

// occurrencesOf returns every token that names target — its declaration name
// (when includeDecl is set) and every value-position identifier that resolves to
// it, wherever it sits in an expression. This is the reverse of resolution.
func occurrencesOf(doc *semantic.Document, target *ir.Const, trees map[cst.Green]cst.Tree, includeDecl bool) []cst.Tree {
	var tokens []cst.Tree

	if includeDecl {
		if declTree, ok := trees[target.Syntax.Syntax()]; ok {
			if nameTok, ok := nameToken(declTree); ok {
				tokens = append(tokens, nameTok)
			}
		}
	}

	for _, c := range doc.Module().Consts {
		if c.Syntax.Value == nil {
			continue
		}
		ast.WalkValueIdents(c.Syntax.Value, func(id *ast.Identifier) {
			if doc.Resolve(id) == target {
				if t, ok := trees[id.Syntax()]; ok {
					tokens = append(tokens, t)
				}
			}
		})
	}
	return tokens
}

// references returns the locations of every reference to the symbol at offset
// (including its declaration when includeDecl is set).
func references(doc *semantic.Document, offset int, uri protocol.DocumentURI, includeDecl bool) []protocol.Location {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok {
		return nil
	}

	var locations []protocol.Location
	for _, tok := range occurrencesOf(doc, occ.target, trees, includeDecl) {
		locations = append(locations, protocol.Location{URI: uri, Range: toRange(buf, tok.Offset(), tok.End())})
	}
	return locations
}

// rename renames the symbol at offset to newName at its declaration and every
// reference. It returns nil if there is no symbol under the cursor or newName is
// not a valid identifier.
func rename(doc *semantic.Document, offset int, newName string, uri protocol.DocumentURI) *protocol.WorkspaceEdit {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok || !isIdentifier(newName) {
		return nil
	}

	var edits []protocol.TextEdit
	for _, tok := range occurrencesOf(doc, occ.target, trees, true) {
		edits = append(edits, protocol.TextEdit{Range: toRange(buf, tok.Offset(), tok.End()), NewText: newName})
	}
	if len(edits) == 0 {
		return nil
	}
	return &protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentURI][]protocol.TextEdit{uri: edits},
	}
}

// documentHighlights returns every occurrence of the symbol under the cursor as
// a highlight: its declaration as a write, each value reference as a read. It is
// occurrencesOf rendered for the in-file "highlight all uses" feature.
func documentHighlights(doc *semantic.Document, offset int) []protocol.DocumentHighlight {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

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
func prepareRename(doc *semantic.Document, offset int) *protocol.PrepareRenameResult {
	trees := positionedTrees(doc.AST().Concrete().Tree())
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
