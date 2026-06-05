package lsp

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// The editor side of top-level functions: call-snippet completion in value
// positions, the hover card for a declaration or a call site, and go-to-def
// from a callee to its declaration.

// functionItems is one completion item per declared function, labelled with
// the name, detailed with the signature, documented with the doc comment, and
// inserted as a call snippet with a tab stop per argument.
func functionItems(doc view) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindFunction
	snippet := protocol.InsertTextFormatSnippet
	var items []protocol.CompletionItem
	seen := map[string]bool{}
	for _, f := range doc.Module().Funcs {
		if f.Name == "" || seen[f.Name] {
			continue
		}
		seen[f.Name] = true

		item := protocol.CompletionItem{Label: f.Name, Kind: &kind, Detail: funcSignature(f)}
		if len(f.Doc) > 0 {
			item.Documentation = &protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: strings.Join(f.Doc, "\n"),
			}
		}
		item.InsertText = funcCallSnippet(f)
		item.InsertTextFormat = &snippet
		items = append(items, item)
	}
	return items
}

// funcCallSnippet renders a function as an insertable call, exactly as a
// method's callSnippet does: each parameter a tab stop, a function-typed one
// expanded to a fn literal.
func funcCallSnippet(f *ir.Function) string {
	var b strings.Builder
	b.WriteString(f.Name)
	b.WriteString("(")
	appendParamsSnippet(&b, f.Params, nil)
	b.WriteString(")")
	return b.String()
}

// funcSignature renders a function as it is declared: modifiers, name,
// parameters, and result, in source syntax.
func funcSignature(f *ir.Function) string {
	var b strings.Builder
	if f.Public {
		b.WriteString("pub ")
	}
	b.WriteString("fn ")
	b.WriteString(f.Name)
	b.WriteString("(")
	for i, p := range f.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		if p.Type != nil {
			b.WriteString(": " + p.Type.String())
		}
	}
	b.WriteString(")")
	if f.Result != nil {
		b.WriteString(": " + f.Result.String())
	}
	return b.String()
}

// funcAt resolves the function denoted at offset: the declared name in a
// FuncDecl header, or a call's callee identifier that names a function. It
// returns the module's resolved function.
func funcAt(doc view, offset int) (*ir.Function, cst.Tree, bool) {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil, cst.Tree{}, false
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil, cst.Tree{}, false
	}
	pk, isNode := parent.Kind()
	if !isNode {
		return nil, cst.Tree{}, false
	}
	parentNode, _ := parent.Node()

	switch pk {
	case cst.FuncDecl:
		// The declaration's own name: the function backed by this very node.
		for _, f := range doc.Module().Funcs {
			if f.Syntax != nil && f.Syntax.Syntax() == parentNode {
				return f, leaf, true
			}
		}
	case cst.NameRef:
		// A callee: the identifier backed by this NameRef, resolved to the
		// function it calls. Operators never desugar through a NameRef callee,
		// so only a written call matches.
		var id *ast.Identifier
		forEachExpr(doc.AST().File(), func(e ast.Expr) {
			if c, ok := e.(*ast.CallExpr); ok {
				if i, ok := c.Callee.(*ast.Identifier); ok && i.Syntax() == parentNode {
					id = i
				}
			}
		})
		if id == nil {
			return nil, cst.Tree{}, false
		}
		fd := doc.ResolveFunc(id)
		if fd == nil {
			return nil, cst.Tree{}, false
		}
		for _, f := range doc.Module().Funcs {
			if f.Syntax == fd {
				return f, leaf, true
			}
		}
	}
	return nil, cst.Tree{}, false
}

// funcHover describes the function denoted at offset — its declared name or a
// call site — as its signature and doc; or, with the cursor on a parameter of
// a function declaration, the parameter's name and type.
func funcHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	if f, leaf, ok := funcAt(doc, offset); ok {
		var b strings.Builder
		b.WriteString("```masterbelt\n")
		b.WriteString(funcSignature(f))
		b.WriteString("\n```")
		if len(f.Doc) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(f.Doc, "\n"))
		}
		r := toRange(buf, leaf.Offset(), leaf.End())
		return &protocol.Hover{
			Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
			Range:    &r,
		}
	}

	// A function's parameter: its declaration in the header, or a use in the
	// body — the same card a method parameter shows.
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}
	for _, f := range doc.Module().Funcs {
		if f.Syntax == nil {
			continue
		}
		ft, found := trees[f.Syntax.Syntax()]
		if !found || !within(ft, offset) {
			continue
		}
		for _, p := range f.Params {
			if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
				continue
			}
			r := toRange(buf, tok.Offset(), tok.End())
			return &protocol.Hover{
				Contents: protocol.MarkupContent{
					Kind:  protocol.Markdown,
					Value: "```masterbelt\n" + name + ": " + p.Type.String() + "\n```",
				},
				Range: &r,
			}
		}
	}
	return nil
}
