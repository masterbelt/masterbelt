package lsp

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// hover returns documentation and type information for the symbol at offset:
// hovering a declaration's name describes that constant; hovering a value
// reference describes the constant it resolves to.
func hover(doc *semantic.Document, offset int) *protocol.Hover {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	for _, c := range doc.Module().Consts {
		decl := c.Syntax

		if ref, ok := decl.Value.(*ast.NameRef); ok {
			if t, ok := trees[ref.Syntax()]; ok && within(t, offset) {
				if target := referenceTarget(c); target != nil {
					return constHover(target, buf, t)
				}
				return nil
			}
		}

		if declTree, ok := trees[decl.Syntax()]; ok {
			if nameTok, ok := nameToken(declTree); ok && within(nameTok, offset) {
				return constHover(c, buf, nameTok)
			}
		}
	}
	return nil
}

// definition resolves the reference at offset to the location of its target
// declaration's name.
func definition(doc *semantic.Document, offset int, uri protocol.DocumentURI) []protocol.Location {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	for _, c := range doc.Module().Consts {
		ref, ok := c.Syntax.Value.(*ast.NameRef)
		if !ok {
			continue
		}
		t, ok := trees[ref.Syntax()]
		if !ok || !within(t, offset) {
			continue
		}

		target := referenceTarget(c)
		if target == nil {
			return nil
		}
		targetTree, ok := trees[target.Syntax.Syntax()]
		if !ok {
			return nil
		}
		rng := targetTree
		if nameTok, ok := nameToken(targetTree); ok {
			rng = nameTok
		}
		return []protocol.Location{{URI: uri, Range: toRange(buf, rng.Offset(), rng.End())}}
	}
	return nil
}

func referenceTarget(c *ir.Const) *ir.Const {
	if ref, ok := c.Value.(*ir.Reference); ok {
		return ref.Target
	}
	return nil
}

// constHover renders a constant's signature (modifiers, name, inferred type) and
// its doc comments as Markdown, with the hovered token as the hover's range.
func constHover(c *ir.Const, buf source.Buffer, rng cst.Tree) *protocol.Hover {
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	if c.Public {
		b.WriteString("pub ")
	}
	b.WriteString("const ")
	b.WriteString(c.Name)
	if c.Type != ir.Invalid {
		b.WriteString(": ")
		b.WriteString(c.Type.String())
	}
	b.WriteString("\n```")
	if len(c.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(c.Doc, "\n"))
	}

	r := toRange(buf, rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}
