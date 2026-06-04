package lsp

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// hover returns documentation and type information for the symbol at offset:
// hovering a declaration's name describes that constant; hovering a value
// reference (including one nested in an expression) describes the constant it
// resolves to; hovering a function-literal parameter — its declaration or a
// use in the body — shows its (possibly inferred) type; hovering an assertion
// (its keyword, or any spot in the condition no more specific hover claims)
// shows its power-assert diagram.
func hover(doc view, offset int) *protocol.Hover {
	trees := doc.Trees()
	if occ, ok := occurrenceAt(doc, offset, trees); ok {
		return constHover(occ.target, doc.Buffer(), occ.token)
	}
	if h := lambdaParamHover(doc, offset, trees); h != nil {
		return h
	}
	return assertHover(doc, offset, trees)
}

// definition resolves the reference at offset to the location of its target
// declaration's name — in this file, or in the sibling file an import brought
// it from.
func definition(doc view, offset int) []protocol.Location {
	occ, ok := occurrenceAt(doc, offset, doc.Trees())
	if !ok {
		return nil
	}
	target, ok := doc.viewOf(occ.target)
	if !ok {
		return nil
	}

	trees := target.Trees()
	targetTree, ok := trees[occ.target.Syntax.Syntax()]
	if !ok {
		return nil
	}
	rng := targetTree
	if nameTok, ok := nameToken(targetTree); ok {
		rng = nameTok
	}
	return []protocol.Location{{URI: target.uri, Range: toRange(target.Buffer(), rng.Offset(), rng.End())}}
}

// assertHover renders the assertion at offset as its power-assert diagram —
// the condition with every sub-expression's folded value beneath it — so a
// holding assertion's values are visible without making it fail. The module
// carries the diagram precomputed (ir.Assert), the very values the assertion
// was checked with.
func assertHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	for _, a := range doc.Module().Asserts {
		tree, ok := trees[a.Syntax.Syntax()]
		if !ok {
			continue
		}
		// Anchor at the assert keyword, past the declaration's leading trivia
		// (newlines and doc comments hover nothing).
		start := tree.Offset()
		for _, child := range tree.Children() {
			if k, isTok := child.TokenKind(); isTok && k == token.Assert {
				start = child.Offset()
				break
			}
		}
		if offset < start || offset >= tree.End() {
			continue
		}

		var b strings.Builder
		b.WriteString("```\nassert ")
		lines := strings.Split(a.Diagram, "\n")
		b.WriteString(lines[0])
		for _, line := range lines[1:] {
			b.WriteString("\n       ") // align under the condition, past "assert "
			b.WriteString(line)
		}
		b.WriteString("\n```")
		if len(a.Doc) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(a.Doc, "\n"))
		}

		r := toRange(doc.Buffer(), start, tree.End())
		return &protocol.Hover{
			Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
			Range:    &r,
		}
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
