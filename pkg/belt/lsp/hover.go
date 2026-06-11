package lsp

import (
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
	// A let-bound local is checked first: inside a body it shadows a same-named
	// top-level constant, so its type wins over the constant's card. Outside any
	// body (and for a name no let binds), the lookup yields nothing and the chain
	// falls through to the constant and the other hovers.
	if h := letHover(doc, offset, trees); h != nil {
		return h
	}
	if occ, ok := occurrenceAt(doc, offset, trees); ok {
		return constHover(occ.target, doc.Buffer(), occ.token)
	}
	if t, leaf, ok := typeAt(doc, offset); ok {
		return typeHover(t, doc.Buffer(), leaf)
	}
	if h := enumMemberHover(doc, offset); h != nil {
		return h
	}
	if h := assocConstHover(doc, offset); h != nil {
		return h
	}
	if h := staticCallHover(doc, offset); h != nil {
		return h
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
	if h := funcHover(doc, offset, trees); h != nil {
		return h
	}
	if h := recordFieldHover(doc, offset); h != nil {
		return h
	}
	if h := literalHover(doc, offset); h != nil {
		return h
	}
	if h := rangeLitHover(doc, offset); h != nil {
		return h
	}
	return assertHover(doc, offset, trees)
}

// definition resolves the reference at offset — a value reference or a type
// name — to the location of its target declaration's name, in this file or in
// the sibling file an import brought it from.
func definition(doc view, offset int) []protocol.Location {
	if occ, ok := occurrenceAt(doc, offset, doc.Trees()); ok {
		return declLocation(doc.viewOf(occ.target))(occ.target.Syntax.Syntax())
	}
	if t, _, ok := typeAt(doc, offset); ok {
		if decl := t.DeclSyntax(); decl != nil {
			return declLocation(doc.viewOfType(t))(decl.Syntax())
		}
	}
	if fns, _, ok := funcAt(doc, offset); ok {
		// Every overload is a target, in its own file — an imported callee
		// jumps to the exporter.
		var locs []protocol.Location
		for _, f := range fns {
			if f.Syntax != nil {
				locs = append(locs, declLocation(doc.viewOfFunc(f.Syntax))(f.Syntax.Syntax())...)
			}
		}
		return locs
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
	case cst.TypeDecl, cst.InterfaceDecl, cst.EnumDecl, cst.MasterDecl:
		// The declaration's own name — a type, interface, enum, or master, each of
		// which resolves to a TypeDef. The file's own definitions lead TypeNames, so
		// the name finds the local declaration, not an import it shadows.
		if t := findTypeDef(doc.TypeNames(), name); t != nil {
			return t, leaf, true
		}
	case cst.TypeName:
		return typeAtTypeName(doc, parent, leaf, buf, name)
	case cst.NameRef:
		return typeAtNameRef(doc, parent, leaf, name)
	default:
		// Any other parent kind is not a type-name position (a declaration
		// name, a type expression, or a conversion callee): nothing resolves.
	}
	return nil, cst.Tree{}, false
}

// typeAtTypeName resolves a type-expression name to its definition: a dotted
// name's member (geo.Point) through the qualifier's namespace, a plain name
// through the file's type names. The qualifier of a dotted name resolves to
// nothing — it denotes a namespace, not a type.
func typeAtTypeName(doc view, parent, leaf cst.Tree, buf source.Buffer, name string) (*ir.TypeDef, cst.Tree, bool) {
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
	return nil, cst.Tree{}, false
}

// typeAtNameRef resolves a conversion's callee (error("msg")): a call's callee
// that names a type hovers as the type it constructs. The type rules give the
// type the same priority over a same-named function.
func typeAtNameRef(doc view, parent, leaf cst.Tree, name string) (*ir.TypeDef, cst.Tree, bool) {
	parentNode, isNode := parent.Node()
	if !isNode {
		return nil, cst.Tree{}, false
	}
	isCallee := false
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if c, ok := e.(*ast.CallExpr); ok {
			if i, ok := c.Callee.(*ast.Identifier); ok && i.Syntax() == parentNode {
				isCallee = true
			}
		}
	})
	if isCallee {
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
	// A master is its own kind of declaration — the master keyword, its row record,
	// and its primary key — so it renders its own signature rather than aliasing a
	// type the way the other kinds do.
	if t.Master != nil {
		writeMasterSignature(&b, t)
	} else {
		writeTypeSignature(&b, t)
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

// writeTypeSignature writes the signature line of a type, enum, or interface: the
// keyword, name, generic parameters, and body (or, for an interface, its
// supertraits), then the refinement and implemented interfaces.
func writeTypeSignature(b *strings.Builder, t *ir.TypeDef) {
	// An interface declares a behaviour rather than aliasing a type, so it leads
	// with the interface keyword and shows no body.
	if t.Interface != nil {
		b.WriteString("interface ")
	} else {
		b.WriteString("type ")
	}
	b.WriteString(t.Name)
	b.WriteString(typeParamList(t.Params))
	if t.Interface == nil {
		switch {
		case t.Builtin:
			b.WriteString(" = builtin")
		case t.Body != nil:
			b.WriteString(" = " + t.Body.String())
		}
	} else if len(t.Interface.Parents) > 0 {
		// A child interface shows its supertraits right on the signature, in the
		// declaration's own form (interface orderable: comparable), so the card
		// reads like the source.
		parents := make([]string, len(t.Interface.Parents))
		for i, p := range t.Interface.Parents {
			parents[i] = p.String()
		}
		b.WriteString(": " + strings.Join(parents, ", "))
	}
	if t.Where != nil {
		// The refinement predicate in its canonical surface form — the values
		// the type admits, right on the signature.
		b.WriteString(" where " + ast.Render(t.WhereSyntax()))
	}
	// The interfaces the type implements, right on the signature card.
	for _, impl := range t.Impls {
		b.WriteString(" impl " + impl.String())
	}
}

// writeMasterSignature writes a master's signature laid out like its declaration
// — the master keyword and name, its row record one field per line, and its
// primary key in the source's single-or-parenthesised form — so the card reads
// like the source rather than as a bare type. The row is projected with recordOf
// (the row is a record or a record alias, never opaque), and a row that did not
// resolve renders as an empty record rather than dropping the card.
func writeMasterSignature(b *strings.Builder, t *ir.TypeDef) {
	b.WriteString("master ")
	b.WriteString(t.Name)
	b.WriteString(" {\n  record {")
	if rec, ok := recordOf(t.Master.Row); ok && len(rec.Fields) > 0 {
		b.WriteString("\n")
		for _, f := range rec.Fields {
			b.WriteString("    " + f.Name + ": " + f.Type.String() + "\n")
		}
		b.WriteString("  }")
	} else {
		b.WriteString("}")
	}
	if len(t.Master.Primary) == 1 {
		b.WriteString("\n  primary " + t.Master.Primary[0])
	} else if len(t.Master.Primary) > 1 {
		b.WriteString("\n  primary (" + strings.Join(t.Master.Primary, ", ") + ")")
	}
	b.WriteString("\n}")
}
