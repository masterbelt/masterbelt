package lsp

import (
	"sort"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// References, rename, and prepare-rename are all the reverse of the resolver:
// given the constant under the cursor, find every name that denotes it — its
// declaration and the value references that resolve to it. The resolved IR makes
// that a single pass: a Reference already points at its target *Const.

// references returns the locations of every reference to the symbol at offset
// — a constant or a type — across every file of the workspace (including its
// declaration when includeDecl is set).
func references(doc view, offset int, includeDecl bool) []protocol.Location {
	var ranges map[protocol.DocumentURI][]protocol.Range
	if occ, ok := occurrenceAt(doc, offset, doc.Trees()); ok {
		ranges = programOccurrences(doc, occ.target, includeDecl)
	} else if t, _, ok := typeAt(doc, offset); ok {
		ranges = programTypeOccurrences(doc, t, includeDecl)
	} else {
		return nil
	}

	var locations []protocol.Location
	for uri, rs := range ranges {
		for _, r := range rs {
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

// rename renames the symbol at offset — a constant or a type — to newName at
// its declaration and every reference, across every file of the workspace. It
// returns nil if there is no symbol under the cursor, newName is not a valid
// identifier, or the declaration lies outside the workspace (the prelude's).
func rename(doc view, offset int, newName string) *protocol.WorkspaceEdit {
	if !isIdentifier(newName) {
		return nil
	}
	var ranges map[protocol.DocumentURI][]protocol.Range
	if occ, ok := occurrenceAt(doc, offset, doc.Trees()); ok {
		ranges = programOccurrences(doc, occ.target, true)
	} else if t, _, ok := typeAt(doc, offset); ok {
		if _, declared := doc.viewOfType(t); !declared {
			return nil // a prelude type cannot be renamed
		}
		ranges = programTypeOccurrences(doc, t, true)
	} else {
		return nil
	}

	changes := map[protocol.DocumentURI][]protocol.TextEdit{}
	for uri, rs := range ranges {
		for _, r := range rs {
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

// documentHighlights returns every occurrence of the symbol under the cursor —
// a constant or a type — as a highlight: its declaration as a write, each
// reference as a read.
func documentHighlights(doc view, offset int) []protocol.DocumentHighlight {
	buf := doc.Buffer()
	trees := doc.Trees()

	var declSyntax *cst.Node
	var reads []cst.Tree
	if occ, ok := occurrenceAt(doc, offset, trees); ok {
		declSyntax = occ.target.Syntax.Syntax()
		reads = occurrencesOf(doc, occ.target, trees, false)
	} else if t, _, ok := typeAt(doc, offset); ok {
		if t.Syntax != nil {
			declSyntax = t.Syntax.Syntax()
		}
		reads = typeOccurrencesOf(doc, t, trees, false)
	} else {
		return nil
	}

	write := protocol.DocumentHighlightKindWrite
	read := protocol.DocumentHighlightKindRead

	var highlights []protocol.DocumentHighlight
	if declTree, ok := trees[declSyntax]; ok {
		if nameTok, ok := nameToken(declTree); ok {
			highlights = append(highlights, protocol.DocumentHighlight{
				Range: toRange(buf, nameTok.Offset(), nameTok.End()),
				Kind:  &write,
			})
		}
	}
	for _, tok := range reads {
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
	if occ, ok := occurrenceAt(doc, offset, trees); ok {
		return &protocol.PrepareRenameResult{
			Range:       toRange(doc.Buffer(), occ.token.Offset(), occ.token.End()),
			Placeholder: occ.target.Name,
		}
	}
	if t, leaf, ok := typeAt(doc, offset); ok {
		if _, declared := doc.viewOfType(t); !declared {
			return nil // a prelude type cannot be renamed
		}
		return &protocol.PrepareRenameResult{
			Range:       toRange(doc.Buffer(), leaf.Offset(), leaf.End()),
			Placeholder: t.Name,
		}
	}
	return nil
}

// isIdentifier reports whether s is a valid masterbelt identifier and not a
// reserved word.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := c >= '0' && c <= '9'
		if (i == 0 && !letter) || (!letter && !digit) {
			return false
		}
	}
	return token.Lookup(s) == token.Ident
}
