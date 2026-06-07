package lsp

import (
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// The editor side of top-level functions: call-snippet completion in value
// positions, the hover card for a declaration or a call site, and go-to-def
// from a callee to its declaration.

// functionItems is one completion item per declared function, labelled with
// the name, detailed with the signature, documented with the doc comment, and
// inserted as a call snippet with a tab stop per argument.
func functionItems(doc view, effectful bool) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindFunction
	snippet := protocol.InsertTextFormatSnippet
	var items []protocol.CompletionItem
	seen := map[*ir.Function]bool{}
	for _, f := range doc.FunctionsInScope() {
		if f.Name == "" || seen[f] {
			continue
		}
		seen[f] = true
		if !effectful && len(f.Effects) > 0 {
			continue // a pure position cannot call an effectful function
		}

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
	if f.Extern {
		b.WriteString("extern ")
	}
	b.WriteString("fn ")
	for _, eff := range f.Effects {
		b.WriteString(eff + " ")
	}
	b.WriteString(f.Name)
	b.WriteString(typeParamList(f.TypeParams))
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

// typeParamList renders a generic parameter list as it is declared — "<T>",
// "<T: foldable<int>>", "<T, U>" — or "" when there are none. It is shared by a
// function's and a type's signature card, so a bound shows the interface the
// parameter is fixed to.
func typeParamList(params []*ir.TypeParam) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = p.Name
		if p.Bound != nil {
			parts[i] += ": " + p.Bound.String()
		}
	}
	return "<" + strings.Join(parts, ", ") + ">"
}

// funcAt resolves the functions denoted at offset: the declared name in a
// FuncDecl header (that one declaration), a call's callee identifier (the
// whole overload set it names, local or imported), or the member name of a
// namespace function call (geo.area). It returns the program's resolved
// functions — the very shells the owning modules publish.
func funcAt(doc view, offset int) ([]*ir.Function, cst.Tree, bool) {
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
		return funcAtDecl(doc, parentNode, leaf)
	case cst.NameRef:
		return funcAtCallee(doc, parentNode, leaf)
	case cst.MemberExpr:
		return funcAtMember(doc, parentNode, leaf)
	default:
		// Any other parent kind does not denote a function (a declaration
		// name, a callee, or a namespace member): no functions are resolved.
		return nil, cst.Tree{}, false
	}
}

// funcShellsOf maps the candidate declarations to the program's resolved
// function shells, dropping any the owning module did not publish.
func funcShellsOf(doc view, cands []*ast.FuncDecl) []*ir.Function {
	var fns []*ir.Function
	for _, fd := range cands {
		if f := doc.FunctionOf(fd); f != nil {
			fns = append(fns, f)
		}
	}
	return fns
}

// funcAtDecl resolves a FuncDecl header name to the function backed by this
// very node.
func funcAtDecl(doc view, parentNode *cst.Node, leaf cst.Tree) ([]*ir.Function, cst.Tree, bool) {
	for _, f := range doc.Module().Funcs {
		if f.Syntax != nil && f.Syntax.Syntax() == parentNode {
			return []*ir.Function{f}, leaf, true
		}
	}
	return nil, cst.Tree{}, false
}

// funcAtCallee resolves a callee identifier (backed by this NameRef) to the
// overload set it calls. Operators never desugar through a NameRef callee, so
// only a written call matches.
func funcAtCallee(doc view, parentNode *cst.Node, leaf cst.Tree) ([]*ir.Function, cst.Tree, bool) {
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
	if fns := funcShellsOf(doc, doc.ResolveFunc(id)); len(fns) > 0 {
		return fns, leaf, true
	}
	return nil, cst.Tree{}, false
}

// funcAtMember resolves a namespace function call's member name (the area of
// geo.area(...)), when the member access is a call's callee. The receiver's
// qualifier hovers as a namespace elsewhere; only the member denotes functions.
func funcAtMember(doc view, parentNode *cst.Node, leaf cst.Tree) ([]*ir.Function, cst.Tree, bool) {
	var member *ast.MemberExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if c, ok := e.(*ast.CallExpr); ok {
			if m, ok := c.Callee.(*ast.MemberExpr); ok && m.Syntax() == parentNode {
				member = m
			}
		}
	})
	if member == nil {
		return nil, cst.Tree{}, false
	}
	// Only the member-name token denotes the function, not the qualifier.
	if recv, ok := member.Receiver.(*ast.Identifier); ok && leaf.Text(doc.Buffer()) == recv.Name {
		return nil, cst.Tree{}, false
	}
	if fns := funcShellsOf(doc, doc.ResolveFuncMember(member)); len(fns) > 0 {
		return fns, leaf, true
	}
	return nil, cst.Tree{}, false
}

// funcHover describes the functions denoted at offset — a declared name or a
// call site, every overload listed under its own doc, the card an overloaded
// method shows — or, with the cursor on a parameter of a function declaration,
// the parameter's name and type.
func funcHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	if fns, leaf, ok := funcAt(doc, offset); ok {
		var b strings.Builder
		b.WriteString("```masterbelt\n")
		for i, f := range fns {
			if i > 0 && len(f.Doc) > 0 {
				b.WriteString("\n")
			}
			for _, doc := range f.Doc {
				b.WriteString("/// " + doc + "\n")
			}
			b.WriteString(funcSignature(f))
			b.WriteString("\n")
		}
		b.WriteString("```")
		if len(fns) == 1 && len(fns[0].Doc) > 0 {
			// The common single-signature card keeps its doc below the code.
			b.Reset()
			b.WriteString("```masterbelt\n")
			b.WriteString(funcSignature(fns[0]))
			b.WriteString("\n```\n\n")
			b.WriteString(strings.Join(fns[0].Doc, "\n"))
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
