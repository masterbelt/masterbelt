package lsp

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/formatter"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// This file converts between masterbelt's byte-offset model and LSP's
// line/character model. LSP positions count characters in UTF-16 code units,
// which is exactly the encoding our source.Buffer was built to translate — so
// every conversion routes through Buffer.LineColumn / Buffer.OffsetAt with
// source.UTF16Encoding, and nothing here has to understand encodings itself.

// toPosition maps a byte offset to an LSP position.
func toPosition(buf source.Buffer, offset int) protocol.Position {
	line, char := buf.LineColumn(offset, source.UTF16Encoding)
	return protocol.Position{Line: line, Character: char}
}

// toRange maps a byte span to an LSP range.
func toRange(buf source.Buffer, start, end int) protocol.Range {
	return protocol.Range{Start: toPosition(buf, start), End: toPosition(buf, end)}
}

// fromPosition maps an LSP position back to a byte offset.
func fromPosition(buf source.Buffer, p protocol.Position) int {
	return buf.OffsetAt(p.Line, p.Character, source.UTF16Encoding)
}

// toDiagnostics renders every diagnostic of the document — lexer, parser, and
// semantic — as LSP diagnostics, ordered by position. The result is never nil:
// publishing an empty array is how the server clears stale diagnostics.
func toDiagnostics(doc *semantic.Document) []protocol.Diagnostic {
	buf := doc.Buffer()

	var raw []diagnostic.Diagnostic
	raw = append(raw, doc.AST().Concrete().LexDiagnostics()...)
	raw = append(raw, doc.AST().Diagnostics()...)
	raw = append(raw, doc.Diagnostics()...)
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].Offset < raw[j].Offset })

	out := make([]protocol.Diagnostic, 0, len(raw))
	for _, d := range raw {
		severity := toSeverity(d.Severity)
		out = append(out, protocol.Diagnostic{
			Range:    toRange(buf, d.Offset, d.End()),
			Severity: &severity,
			Source:   "masterbelt",
			Message:  d.Message,
		})
	}
	return out
}

func toSeverity(s diagnostic.Severity) protocol.DiagnosticSeverity {
	switch s {
	case diagnostic.Warning:
		return protocol.SeverityWarning
	case diagnostic.Info:
		return protocol.SeverityInformation
	case diagnostic.Hint:
		return protocol.SeverityHint
	default:
		return protocol.SeverityError
	}
}

// documentSymbols turns the program's constants into an LSP outline. Each
// constant becomes a symbol whose Detail is its inferred type, whose Range
// covers the whole declaration, and whose SelectionRange covers just the name —
// the part an editor highlights when you pick the symbol.
func documentSymbols(doc *semantic.Document) []protocol.DocumentSymbol {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	var symbols []protocol.DocumentSymbol
	for _, c := range doc.Module().Consts {
		declTree, ok := trees[c.Syntax.Syntax()]
		if !ok {
			continue
		}

		name := c.Name
		if name == "" {
			name = "<anonymous>"
		}
		selection := toRange(buf, declTree.Offset(), declTree.End())
		if nameTok, ok := nameToken(declTree); ok {
			selection = toRange(buf, nameTok.Offset(), nameTok.End())
		}
		detail := ""
		if c.Type != ir.Invalid {
			detail = ": " + c.Type.String()
		}

		symbols = append(symbols, protocol.DocumentSymbol{
			Name:           name,
			Detail:         detail,
			Kind:           protocol.SymbolKindConstant,
			Range:          toRange(buf, declTree.Offset(), declTree.End()),
			SelectionRange: selection,
		})
	}
	return symbols
}

// positionedTrees maps each green node of the concrete tree to its positioned
// view, so an AST node's green Syntax can be turned back into a source range.
func positionedTrees(root cst.Tree) map[cst.Green]cst.Tree {
	trees := map[cst.Green]cst.Tree{}
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		trees[t.Green()] = t
		for _, child := range t.Children() {
			walk(child)
		}
	}
	walk(root)
	return trees
}

// within reports whether offset falls inside an element's byte span.
func within(t cst.Tree, offset int) bool {
	return t.Offset() <= offset && offset < t.End()
}

// nameToken returns the positioned identifier that names the declaration — its
// only direct Ident child (the type and value identifiers are nested deeper).
func nameToken(decl cst.Tree) (cst.Tree, bool) {
	for _, child := range decl.Children() {
		if tok, ok := child.Token(); ok && tok.Kind() == token.Ident {
			return child, true
		}
	}
	return cst.Tree{}, false
}

// formatEdits returns the edits to format the document, or nil if it is already
// formatted. The formatting policy lives in source/formatter (which works on the
// lossless concrete tree, so comments are preserved); the server only diffs the
// result against the current text and, on a change, returns it as a single
// whole-document replacement.
func formatEdits(doc *abstract.Document) []protocol.TextEdit {
	buf := doc.Buffer()
	original := string(buf.Slice(0, buf.Len()))
	formatted := formatter.Format(buf, doc.Concrete().Root())
	if formatted == original {
		return nil
	}
	return []protocol.TextEdit{{
		Range:   toRange(buf, 0, buf.Len()),
		NewText: formatted,
	}}
}
