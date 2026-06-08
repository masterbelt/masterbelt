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

	raw := make([]diagnostic.Diagnostic, 0, len(doc.AST().Concrete().LexDiagnostics())+len(doc.AST().Diagnostics())+len(doc.Diagnostics()))
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

	symbols := make([]symbolBuilder, 0, len(doc.Module().Consts))
	symbols = append(symbols, constSymbols(doc)...)
	symbols = append(symbols, enumSymbols(doc)...)
	symbols = append(symbols, interfaceSymbols(doc)...)
	symbols = append(symbols, typeMemberSymbols(doc)...)
	symbols = append(symbols, funcSymbols(doc)...)

	out := make([]protocol.DocumentSymbol, 0, len(symbols))
	for _, b := range symbols {
		if s, ok := b.build(buf, trees); ok {
			out = append(out, s)
		}
	}
	return out
}

// symbolBuilder is one outline entry resolved into a positioned LSP symbol
// once: a declaration's green node, its display name/detail/kind, and any
// pre-built children (member symbols whose parent owns their layout). The
// build is deferred so that a parent whose own declaration tree is missing is
// dropped together with its children.
type symbolBuilder struct {
	green  *cst.Node
	name   string
	detail string
	// anchor is the declaration's stable address, appended to the detail
	// in the outline so an agent reading the symbol tree sees the name to
	// reference it by; "" for a symbol that carries none (an enum member).
	anchor   string
	kind     protocol.SymbolKind
	children []symbolBuilder
	// dropIfNoChildren marks a container symbol that exists only to hold its
	// members (the plain-type member outline): if every child fails to resolve
	// it is dropped, matching the original where the empty-children skip ran on
	// the post-resolution count.
	dropIfNoChildren bool
}

// build resolves the builder against the document's positioned trees, turning
// it into an LSP DocumentSymbol whose Range covers the whole declaration and
// whose SelectionRange covers just the name. It reports ok=false when the
// declaration's tree is absent, so the caller drops the symbol.
func (b symbolBuilder) build(buf source.Buffer, trees map[cst.Green]cst.Tree) (protocol.DocumentSymbol, bool) {
	declTree, ok := trees[b.green]
	if !ok {
		return protocol.DocumentSymbol{}, false
	}
	var children []protocol.DocumentSymbol
	for _, c := range b.children {
		if cs, ok := c.build(buf, trees); ok {
			children = append(children, cs)
		}
	}
	if b.dropIfNoChildren && len(children) == 0 {
		return protocol.DocumentSymbol{}, false
	}
	name := b.name
	if name == "" {
		name = "<anonymous>"
	}
	selection := toRange(buf, declTree.Offset(), declTree.End())
	if nameTok, ok := nameToken(declTree); ok {
		selection = toRange(buf, nameTok.Offset(), nameTok.End())
	}
	return protocol.DocumentSymbol{
		Name:           name,
		Detail:         appendAnchor(b.detail, b.anchor),
		Kind:           b.kind,
		Range:          toRange(buf, declTree.Offset(), declTree.End()),
		SelectionRange: selection,
		Children:       children,
	}, true
}

// appendAnchor joins a symbol's detail and its anchor for the outline: the
// detail, a middle-dot separator, then the anchor — or either alone when the
// other is empty (an interface symbol has no type detail; an enum member has no
// anchor).
func appendAnchor(detail, anchor string) string {
	switch {
	case anchor == "":
		return detail
	case detail == "":
		return anchor
	default:
		return detail + "  ·  " + anchor
	}
}

// constSymbols outlines every constant, detailed with its inferred type.
func constSymbols(doc view) []symbolBuilder {
	out := make([]symbolBuilder, 0, len(doc.Module().Consts))
	for _, c := range doc.Module().Consts {
		detail := ""
		if c.Type != ir.Invalid {
			detail = ": " + c.Type.String()
		}
		out = append(out, symbolBuilder{
			green:  c.Syntax.Syntax(),
			name:   c.Name,
			detail: detail,
			anchor: c.Anchor,
			kind:   protocol.SymbolKindConstant,
		})
	}
	return out
}

// enumSymbols outlines every enum and, as children, its members detailed with
// their values.
func enumSymbols(doc view) []symbolBuilder {
	var out []symbolBuilder
	for _, t := range doc.Module().Types {
		if t.Enum == nil || t.EnumSyntax == nil {
			continue // only enums carry a member outline; plain types are omitted here
		}
		b := symbolBuilder{
			green:  t.EnumSyntax.Syntax(),
			name:   t.Name,
			detail: ": " + t.Enum.Base,
			anchor: t.Anchor,
			kind:   protocol.SymbolKindEnum,
		}
		for i, m := range t.Enum.Members {
			if i >= len(t.EnumSyntax.Members) {
				break
			}
			memberDetail := ""
			if m.Value != nil {
				memberDetail = "= " + m.Value.String()
			}
			b.children = append(b.children, symbolBuilder{
				green:  t.EnumSyntax.Members[i].Syntax(),
				name:   m.Name,
				detail: memberDetail,
				kind:   protocol.SymbolKindEnumMember,
			})
		}
		out = append(out, b)
	}
	return out
}

// interfaceSymbols outlines every interface and, as children, its methods
// detailed with their signatures. The InterfaceDecl's members and the def's
// methods are in the same order.
func interfaceSymbols(doc view) []symbolBuilder {
	var out []symbolBuilder
	for _, t := range doc.Module().Types {
		if t.Interface == nil || t.InterfaceSyntax == nil {
			continue // interfaces carry a member outline; other types are omitted here
		}
		b := symbolBuilder{
			green:  t.InterfaceSyntax.Syntax(),
			name:   t.Name,
			anchor: t.Anchor,
			kind:   protocol.SymbolKindInterface,
		}
		for i, m := range t.Methods {
			if i >= len(t.InterfaceSyntax.Members) {
				break
			}
			b.children = append(b.children, symbolBuilder{
				green:  t.InterfaceSyntax.Members[i].Syntax(),
				name:   m.Name,
				detail: methodSignature(m),
				anchor: m.Anchor,
				kind:   protocol.SymbolKindMethod,
			})
		}
		out = append(out, b)
	}
	return out
}

// typeMemberSymbols outlines a plain type that has accessors or static fns,
// whose children are those members: a getter/setter as a Property (it
// reads/writes value.name), a static fn as a Function (called Type.name(...)).
// Ordinary instance methods are left out of this outline — they are reached
// through a value, not the type. An enum or interface is already outlined
// above, so it is skipped here.
func typeMemberSymbols(doc view) []symbolBuilder {
	var out []symbolBuilder
	for _, t := range doc.Module().Types {
		if t.Syntax == nil || t.Enum != nil || t.Interface != nil {
			continue
		}
		var children []symbolBuilder
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
			children = append(children, symbolBuilder{
				green:  m.Syntax.Syntax(),
				name:   m.Name,
				detail: methodSignature(m),
				anchor: m.Anchor,
				kind:   kind,
			})
		}
		if len(children) == 0 {
			continue // no accessor or static fn: no type outline to add
		}
		kind := protocol.SymbolKindClass
		if t.Builtin {
			kind = protocol.SymbolKindStruct
		}
		out = append(out, symbolBuilder{
			green:            t.Syntax.Syntax(),
			name:             t.Name,
			anchor:           t.Anchor,
			kind:             kind,
			children:         children,
			dropIfNoChildren: true,
		})
	}
	return out
}

// funcSymbols outlines every top-level function, detailed with its signature.
func funcSymbols(doc view) []symbolBuilder {
	var out []symbolBuilder
	for _, f := range doc.Module().Funcs {
		if f.Syntax == nil {
			continue
		}
		out = append(out, symbolBuilder{
			green:  f.Syntax.Syntax(),
			name:   f.Name,
			detail: funcSignature(f),
			anchor: f.Anchor,
			kind:   protocol.SymbolKindFunction,
		})
	}
	return out
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
