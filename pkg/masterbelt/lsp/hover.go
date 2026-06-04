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
// resolves to; hovering a type name — a reference or the declaration itself —
// describes that type; hovering a function-literal parameter — its declaration
// or a use in the body — shows its (possibly inferred) type; hovering an
// assertion (its keyword, or any spot in the condition no more specific hover
// claims) shows its power-assert diagram.
func hover(doc view, offset int) *protocol.Hover {
	trees := doc.Trees()
	if occ, ok := occurrenceAt(doc, offset, trees); ok {
		return constHover(occ.target, doc.Buffer(), occ.token)
	}
	if t, leaf, ok := typeAt(doc, offset); ok {
		return typeHover(t, doc.Buffer(), leaf)
	}
	if h := lambdaParamHover(doc, offset, trees); h != nil {
		return h
	}
	if h := methodParamHover(doc, offset, trees); h != nil {
		return h
	}
	return assertHover(doc, offset, trees)
}

// methodParamHover describes the method parameter denoted at offset: its name
// in the signature's parameter list, or a reference to it inside the method's
// body. The type comes from the module's resolved signature, so it renders as
// the checker sees it. Function literals nest inside method bodies and their
// parameters shadow the method's — lambdaParamHover runs first.
func methodParamHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	file := doc.AST().File()
	module := doc.Module()
	for i, td := range file.Types {
		if i >= len(module.Types) {
			break
		}
		for j, m := range td.Methods {
			mt, found := trees[m.Syntax()]
			if !found || !within(mt, offset) || j >= len(module.Types[i].Methods) {
				continue
			}
			for _, p := range module.Types[i].Methods[j].Params {
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
	}
	return nil
}

// definition resolves the reference at offset — a value reference or a type
// name — to the location of its target declaration's name, in this file or in
// the sibling file an import brought it from.
func definition(doc view, offset int) []protocol.Location {
	if occ, ok := occurrenceAt(doc, offset, doc.Trees()); ok {
		return declLocation(doc.viewOf(occ.target))(occ.target.Syntax.Syntax())
	}
	if t, _, ok := typeAt(doc, offset); ok && t.Syntax != nil {
		return declLocation(doc.viewOfType(t))(t.Syntax.Syntax())
	}
	return nil
}

// declLocation returns a locator over the target's view: given the declaring
// CST node, it yields the location of the declaration's name there. With no
// view (an unresolved target, or the prelude's) it yields nothing.
func declLocation(target view, ok bool) func(*cst.Node) []protocol.Location {
	return func(decl *cst.Node) []protocol.Location {
		if !ok {
			return nil
		}
		declTree, found := target.Trees()[decl]
		if !found {
			return nil
		}
		rng := declTree
		if nameTok, hasName := nameToken(declTree); hasName {
			rng = nameTok
		}
		return []protocol.Location{{URI: target.uri, Range: toRange(target.Buffer(), rng.Offset(), rng.End())}}
	}
}

// typeAt finds the type name at offset — an identifier in a type expression
// (qualified or not), or a type declaration's own name — and resolves it to
// its definition. The qualifier of a dotted name (geo in geo.Point) names a
// namespace, not a type, and resolves to nothing here.
func typeAt(doc view, offset int) (*ir.TypeDef, cst.Tree, bool) {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil, cst.Tree{}, false
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil, cst.Tree{}, false
	}
	kind, _ := parent.Kind()
	buf := doc.Buffer()
	name := leaf.Text(buf)

	switch kind {
	case cst.TypeDecl:
		// The declaration's own name. The file's own definitions lead
		// TypeNames, so the name finds the local declaration, not an import
		// it shadows.
		if t := findTypeDef(doc.TypeNames(), name); t != nil {
			return t, leaf, true
		}
	case cst.TypeName:
		var idents []cst.Tree
		for _, c := range parent.Children() {
			if k, isTok := c.TokenKind(); isTok && k == token.Ident {
				idents = append(idents, c)
			}
		}
		if len(idents) == 2 {
			if idents[0].Offset() == leaf.Offset() {
				return nil, cst.Tree{}, false // the namespace qualifier
			}
			if t := findTypeDef(doc.QualifiedTypeNames()[idents[0].Text(buf)], name); t != nil {
				return t, leaf, true
			}
			return nil, cst.Tree{}, false
		}
		if t := findTypeDef(doc.TypeNames(), name); t != nil {
			return t, leaf, true
		}
	}
	return nil, cst.Tree{}, false
}

// findTypeDef returns the definition named name, or nil.
func findTypeDef(defs []*ir.TypeDef, name string) *ir.TypeDef {
	for _, t := range defs {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// leafAt descends to the leaf containing offset, returning it and the
// innermost node it sits in.
func leafAt(root cst.Tree, offset int) (leaf, parent cst.Tree, ok bool) {
	node := root
	for {
		child, found := childContaining(node, offset)
		if !found {
			return cst.Tree{}, cst.Tree{}, false
		}
		if _, isTok := child.Token(); isTok {
			return child, node, true
		}
		node = child
	}
}

// typeHover renders a type definition the way a reader wants it: the
// signature (modifiers, name, generic parameters, body), the doc comment, and
// then every method's signature — what the type can do, at a glance. The
// hovered token is the hover's range.
func typeHover(t *ir.TypeDef, buf source.Buffer, rng cst.Tree) *protocol.Hover {
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	if t.Public {
		b.WriteString("pub ")
	}
	b.WriteString("type ")
	b.WriteString(t.Name)
	if len(t.Params) > 0 {
		parts := make([]string, len(t.Params))
		for i, p := range t.Params {
			parts[i] = p.Name
			if p.Bound != nil {
				parts[i] += ": " + p.Bound.String()
			}
		}
		b.WriteString("<" + strings.Join(parts, ", ") + ">")
	}
	switch {
	case t.Builtin:
		b.WriteString(" = builtin")
	case t.Body != nil:
		b.WriteString(" = " + t.Body.String())
	}
	b.WriteString("\n```")
	if len(t.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(t.Doc, "\n"))
	}
	if len(t.Methods) > 0 {
		b.WriteString("\n\n```masterbelt\n")
		for i, m := range t.Methods {
			// Each method renders as declared, its doc comment above it, so
			// the card reads like the impl block itself.
			if i > 0 && len(m.Doc) > 0 {
				b.WriteString("\n")
			}
			for _, doc := range m.Doc {
				b.WriteString("/// " + doc + "\n")
			}
			b.WriteString(methodSignature(m))
			b.WriteString("\n")
		}
		b.WriteString("```")
	}

	r := toRange(buf, rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// methodSignature renders one method as it is declared: modifiers, name,
// parameters, and result, in source syntax.
func methodSignature(m *ir.Method) string {
	var b strings.Builder
	if m.Public {
		b.WriteString("pub ")
	}
	if m.Extern {
		b.WriteString("extern ")
	}
	b.WriteString(m.Name)
	b.WriteString("(")
	for i, p := range m.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		if p.Type != nil {
			b.WriteString(": " + p.Type.String())
		}
	}
	b.WriteString(")")
	if m.Result != nil {
		b.WriteString(": " + m.Result.String())
	}
	return b.String()
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
