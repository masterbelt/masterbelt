package lsp

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
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
	if h := memberHover(doc, offset, trees); h != nil {
		return h
	}
	if h := methodDeclHover(doc, offset, trees); h != nil {
		return h
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
// body. The type comes from the module's resolved signature — each resolved
// method carries its declaration (ir.Method.Syntax), so the pairing holds
// across overloads — and renders as the checker sees it. Function literals
// nest inside method bodies and their parameters shadow the method's —
// lambdaParamHover runs first.
func methodParamHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
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
	if t.Where != nil {
		// The refinement predicate in its canonical surface form — the values
		// the type admits, right on the signature.
		b.WriteString(" where " + ast.Render(t.Where))
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
	return methodSignatureSubst(m, nil)
}

// methodSignatureSubst is methodSignature with the receiver's solved type
// arguments substituted in, so list<int8>.map shows fn(item: int8).
func methodSignatureSubst(m *ir.Method, subst map[string]ir.Type) string {
	render := func(t ir.Type) string {
		if t == nil {
			return ""
		}
		if len(subst) > 0 {
			t = types.Substitute(t, subst)
		}
		return t.String()
	}
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
			b.WriteString(": " + render(p.Type))
		}
	}
	b.WriteString(")")
	if m.Result != nil {
		b.WriteString(": " + render(m.Result))
	}
	return b.String()
}

// memberHover describes the member access at offset: the method the
// receiver's type binds for the name — its signature with the receiver's
// generic arguments substituted in, and its doc — or the record field it
// reads, with its type.
func memberHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MemberExpr {
		return nil
	}
	parentNode, _ := parent.Node()

	// The AST member access backing this node. Operators desugar to synthetic
	// member accesses, but those share their operator's CST node, never a
	// MemberExpr node — only an access written in the source matches here.
	var member *ast.MemberExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if m, ok := e.(*ast.MemberExpr); ok && m.Syntax() == parentNode {
			member = m
		}
	})
	if member == nil {
		return nil
	}

	recv := receiverTypeOf(doc, member.Receiver, trees, offset)
	if recv == nil || recv == ir.Invalid {
		return nil
	}
	name := member.Member.Name
	r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())

	if m, subst, ok := doc.BindMethod(recv, name); ok {
		var b strings.Builder
		b.WriteString("```masterbelt\n")
		b.WriteString(methodSignatureSubst(m, subst))
		b.WriteString("\n```")
		if len(m.Doc) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(m.Doc, "\n"))
		}
		return &protocol.Hover{
			Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
			Range:    &r,
		}
	}
	if f, ok := fieldOf(recv, name); ok {
		return &protocol.Hover{
			Contents: protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: "```masterbelt\n" + f.Name + ": " + f.Type.String() + "\n```",
			},
			Range: &r,
		}
	}
	return nil
}

// receiverTypeOf resolves the type a member access's receiver has: self is
// the enclosing impl's type, an identifier is the constant it names or the
// parameter it denotes, a namespace member is the constant it imports, and a
// chained access is the field's type on the inner receiver. Anything else —
// a collection literal, an operator chain — goes through the real inference
// in the file's top-level scope.
func receiverTypeOf(doc view, e ast.Expr, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		file := doc.AST().File()
		module := doc.Module()
		for i, td := range file.Types {
			if i >= len(module.Types) {
				break
			}
			for _, m := range td.Methods {
				if t, ok := trees[m.Syntax()]; ok && within(t, offset) {
					return &ir.Named{Def: module.Types[i]}
				}
			}
		}
		return nil
	case *ast.Identifier:
		if c := doc.Resolve(e); c != nil {
			return c.Type
		}
		return paramTypeAt(doc, e.Name, trees, offset)
	case *ast.MemberExpr:
		if c := doc.ResolveMember(e); c != nil {
			return c.Type
		}
		if inner := receiverTypeOf(doc, e.Receiver, trees, offset); inner != nil {
			if f, ok := fieldOf(inner, e.Member.Name); ok {
				return f.Type
			}
		}
		return nil
	}
	if t := doc.TypeOfExpr(e); t != ir.Invalid {
		return t
	}
	return nil
}

// paramTypeAt resolves name as a parameter of what encloses offset: the
// innermost function literal first (its parameters shadow the method's),
// then the method's signature. A self-typed parameter resolves to the
// enclosing impl's type, so its methods bind through it.
func paramTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	types := doc.FuncLitTypes()
	var enclosing []*ast.FuncLit
	forEachFuncLit(doc, func(lit *ast.FuncLit) {
		if t, ok := trees[lit.Syntax()]; ok && within(t, offset) {
			enclosing = append(enclosing, lit)
		}
	})
	for i := len(enclosing) - 1; i >= 0; i-- {
		lit := enclosing[i]
		ft := types[lit]
		if ft == nil {
			continue
		}
		for j, p := range lit.Params {
			if p.Name == name && j < len(ft.Params) && ft.Params[j] != ir.Invalid {
				return ft.Params[j]
			}
		}
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, ok := trees[irm.Syntax.Syntax()]
			if !ok || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
				if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
					continue
				}
				if _, isSelf := p.Type.(*ir.SelfType); isSelf {
					return &ir.Named{Def: def}
				}
				return p.Type
			}
		}
	}
	return nil
}

// fieldOf returns the record field a type carries under name — directly, or
// through a named type's record body.
func fieldOf(t ir.Type, name string) (ir.Field, bool) {
	switch t := t.(type) {
	case *ir.Record:
		for _, f := range t.Fields {
			if f.Name == name {
				return f, true
			}
		}
	case *ir.Named:
		if t.Def != nil {
			return fieldOf(t.Def.Body, name)
		}
	}
	return ir.Field{}, false
}

// methodDeclHover describes the method declared at offset — the cursor on its
// name in an impl block — as its resolved signature and doc.
func methodDeclHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MethodDecl {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			var b strings.Builder
			b.WriteString("```masterbelt\n")
			b.WriteString(methodSignature(irm))
			b.WriteString("\n```")
			if len(irm.Doc) > 0 {
				b.WriteString("\n\n")
				b.WriteString(strings.Join(irm.Doc, "\n"))
			}
			r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
				Range:    &r,
			}
		}
	}
	return nil
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
