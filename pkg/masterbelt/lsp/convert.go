package lsp

import (
	"encoding/json"
	"sort"
	"strconv"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/formatter"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
func toDiagnostics(doc view) []protocol.Diagnostic {
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
			// The catalog code (masterbelt.semantic.invalid_operation, ...), so
			// the editor's problems panel identifies the diagnostic beyond its
			// rendered message. The field is int|string in the protocol; ours is
			// always the string form.
			Code:    json.RawMessage(strconv.Quote(string(d.Code))),
			Source:  "masterbelt",
			Message: d.Message,
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

// documentSymbols turns the program's constants and functions into an LSP
// outline. Each declaration becomes a symbol whose Detail is its inferred type
// (or signature), whose Range covers the whole declaration, and whose
// SelectionRange covers just the name — the part an editor highlights when you
// pick the symbol.
func documentSymbols(doc view) []protocol.DocumentSymbol {
	buf := doc.Buffer()
	trees := doc.Trees()

	symbol := func(green *cst.Node, name, detail string, kind protocol.SymbolKind) (protocol.DocumentSymbol, bool) {
		declTree, ok := trees[green]
		if !ok {
			return protocol.DocumentSymbol{}, false
		}
		if name == "" {
			name = "<anonymous>"
		}
		selection := toRange(buf, declTree.Offset(), declTree.End())
		if nameTok, ok := nameToken(declTree); ok {
			selection = toRange(buf, nameTok.Offset(), nameTok.End())
		}
		return protocol.DocumentSymbol{
			Name:           name,
			Detail:         detail,
			Kind:           kind,
			Range:          toRange(buf, declTree.Offset(), declTree.End()),
			SelectionRange: selection,
		}, true
	}

	var symbols []protocol.DocumentSymbol
	for _, c := range doc.Module().Consts {
		detail := ""
		if c.Type != ir.Invalid {
			detail = ": " + c.Type.String()
		}
		if s, ok := symbol(c.Syntax.Syntax(), c.Name, detail, protocol.SymbolKindConstant); ok {
			symbols = append(symbols, s)
		}
	}
	for _, t := range doc.Module().Types {
		if t.Enum == nil || t.EnumSyntax == nil {
			continue // only enums carry a member outline; plain types are omitted here
		}
		detail := ": " + t.Enum.Base
		s, ok := symbol(t.EnumSyntax.Syntax(), t.Name, detail, protocol.SymbolKindEnum)
		if !ok {
			continue
		}
		// Each member is a child symbol, detailed with its value.
		for i, m := range t.Enum.Members {
			if i >= len(t.EnumSyntax.Members) {
				break
			}
			memberDetail := ""
			if m.Value != nil {
				memberDetail = "= " + m.Value.String()
			}
			if ms, ok := symbol(t.EnumSyntax.Members[i].Syntax(), m.Name, memberDetail, protocol.SymbolKindEnumMember); ok {
				s.Children = append(s.Children, ms)
			}
		}
		symbols = append(symbols, s)
	}
	for _, t := range doc.Module().Types {
		if t.Interface == nil || t.InterfaceSyntax == nil {
			continue // interfaces carry a member outline; other types are omitted here
		}
		s, ok := symbol(t.InterfaceSyntax.Syntax(), t.Name, "", protocol.SymbolKindInterface)
		if !ok {
			continue
		}
		// Each member is a child method symbol; the InterfaceDecl's members and
		// the def's methods are in the same order.
		for i, m := range t.Methods {
			if i >= len(t.InterfaceSyntax.Members) {
				break
			}
			if ms, ok := symbol(t.InterfaceSyntax.Members[i].Syntax(), m.Name, methodSignature(m), protocol.SymbolKindMethod); ok {
				s.Children = append(s.Children, ms)
			}
		}
		symbols = append(symbols, s)
	}
	// A plain type with accessors or static fns gets a type symbol whose children
	// are those members: a getter/setter as a Property (it reads/writes
	// value.name), a static fn as a Function (called Type.name(...)). Ordinary
	// instance methods are left out of this outline — they are reached through a
	// value, not the type. An enum or interface is already outlined above.
	for _, t := range doc.Module().Types {
		if t.Syntax == nil || t.Enum != nil || t.Interface != nil {
			continue
		}
		var children []protocol.DocumentSymbol
		for _, m := range t.Methods {
			if m.Syntax == nil {
				continue
			}
			var kind protocol.SymbolKind
			switch m.Kind {
			case ir.MethodGetter, ir.MethodSetter:
				kind = protocol.SymbolKindProperty
			case ir.MethodStatic:
				kind = protocol.SymbolKindFunction
			default:
				continue // an ordinary method is reached through a value, not the type
			}
			if ms, ok := symbol(m.Syntax.Syntax(), m.Name, methodSignature(m), kind); ok {
				children = append(children, ms)
			}
		}
		if len(children) == 0 {
			continue // no accessor or static fn: no type outline to add
		}
		kind := protocol.SymbolKindClass
		if t.Builtin {
			kind = protocol.SymbolKindStruct
		}
		if s, ok := symbol(t.Syntax.Syntax(), t.Name, "", kind); ok {
			s.Children = children
			symbols = append(symbols, s)
		}
	}
	for _, f := range doc.Module().Funcs {
		if f.Syntax == nil {
			continue
		}
		if s, ok := symbol(f.Syntax.Syntax(), f.Name, funcSignature(f), protocol.SymbolKindFunction); ok {
			symbols = append(symbols, s)
		}
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
