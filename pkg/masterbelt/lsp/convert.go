package lsp

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/formatter"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
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

// toDiagnostics renders every diagnostic of the document — lexer and parser
// alike — as LSP diagnostics, ordered by position. The result is never nil:
// publishing an empty array is how the server clears stale diagnostics.
func toDiagnostics(doc *abstract.Document) []protocol.Diagnostic {
	buf := doc.Buffer()

	raw := make([]diagnostic.Diagnostic, 0, len(doc.Diagnostics()))
	raw = append(raw, doc.Concrete().LexDiagnostics()...)
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

// documentSymbols turns the file's declarations into an LSP outline. Each
// constant becomes a symbol whose Range covers the whole declaration (computed
// from the positioned concrete tree) and whose SelectionRange covers just the
// name — the part an editor highlights when you pick the symbol.
func documentSymbols(doc *abstract.Document) []protocol.DocumentSymbol {
	buf := doc.Buffer()

	// The abstract declarations carry the resolved names; pair them with their
	// positioned concrete nodes (same green node, by identity) to get spans.
	byNode := map[*cst.Node]string{}
	details := map[*cst.Node]string{}
	for _, decl := range doc.File().Decls {
		byNode[decl.Syntax()] = decl.Name
		if decl.Type != nil {
			details[decl.Syntax()] = ": " + decl.Type.Name
		}
	}

	var symbols []protocol.DocumentSymbol
	for _, child := range doc.Concrete().Tree().Children() {
		node, ok := child.Node()
		if !ok || node.Kind() != cst.ConstDecl {
			continue
		}

		name := byNode[node]
		if name == "" {
			name = "<anonymous>"
		}
		selection := toRange(buf, child.Offset(), child.End())
		if nameTok, ok := nameToken(child); ok {
			selection = toRange(buf, nameTok.Offset(), nameTok.End())
		}

		symbols = append(symbols, protocol.DocumentSymbol{
			Name:           name,
			Detail:         details[node], // "" when the type is inferred; omitted on the wire
			Kind:           protocol.SymbolKindConstant,
			Range:          toRange(buf, child.Offset(), child.End()),
			SelectionRange: selection,
		})
	}
	return symbols
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
