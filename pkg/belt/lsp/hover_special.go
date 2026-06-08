package lsp

import (
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Hover cards for the special contexts that are not a plain symbol: the
// canonical value of a datetime or duration literal, an assertion's
// power-assert diagram, and a constant's declaration signature.

// literalHover describes the datetime or duration literal at offset by its
// canonical value: the UTC instant an offset spelling normalizes to, or the
// largest-units-first decomposition of a duration (90m is 1h30m). A malformed
// literal — already diagnosed by the lexer — hovers nothing.
func literalHover(doc view, offset int) *protocol.Hover {
	leaf, _, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	kind, isTok := leaf.TokenKind()
	if !isTok {
		return nil
	}
	text := leaf.Text(doc.Buffer())

	var typ string
	var canon *ir.Constant
	switch kind {
	case token.DatetimeLit:
		ms, ok := eval.DatetimeMillis(text)
		if !ok {
			return nil
		}
		typ, canon = "datetime", ir.DatetimeConstant(ms)
	case token.DurationLit:
		ms, ok := eval.DurationMillis(text)
		if !ok {
			return nil
		}
		typ, canon = "duration", ir.DurationConstant(ms)
	default:
		return nil
	}

	r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: "```masterbelt\n" + typ + " = " + canon.String() + "\n```",
		},
		Range: &r,
	}
}

// rangeLitHover describes the range literal at offset — the cursor on its ".."
// or "..." operator — as the range type. A range literal is the surface syntax
// of the range builtin, so hovering its operator reads the same type a range(...)
// call would; the operator is the literal's anchor (its bounds hover as their own
// expressions). The card names the type only — the folded value is not always
// available here (a bound may reference a parameter) — matching how the other
// type cards read.
func rangeLitHover(doc view, offset int) *protocol.Hover {
	leaf, _, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	kind, isTok := leaf.TokenKind()
	if !isTok || (kind != token.DotDot && kind != token.DotDotDot) {
		return nil
	}
	r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: "```masterbelt\nrange\n```",
		},
		Range: &r,
	}
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
