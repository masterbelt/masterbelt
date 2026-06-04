package lsp

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// hover returns documentation and type information for the symbol at offset:
// hovering a declaration's name describes that constant; hovering a value
// reference (including one nested in an expression) describes the constant it
// resolves to; hovering a function-literal parameter — its declaration or a
// use in the body — shows its (possibly inferred) type.
func hover(doc *semantic.Document, offset int) *protocol.Hover {
	trees := positionedTrees(doc.AST().Concrete().Tree())
	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok {
		return lambdaParamHover(doc, offset, trees)
	}
	return constHover(occ.target, doc.Buffer(), occ.token)
}

// definition resolves the reference at offset to the location of its target
// declaration's name.
func definition(doc *semantic.Document, offset int, uri protocol.DocumentURI) []protocol.Location {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())

	occ, ok := occurrenceAt(doc, offset, trees)
	if !ok {
		return nil
	}
	targetTree, ok := trees[occ.target.Syntax.Syntax()]
	if !ok {
		return nil
	}
	rng := targetTree
	if nameTok, ok := nameToken(targetTree); ok {
		rng = nameTok
	}
	return []protocol.Location{{URI: uri, Range: toRange(buf, rng.Offset(), rng.End())}}
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
	if c.Eval != nil {
		b.WriteString(" = ")
		b.WriteString(c.Eval.String())
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
