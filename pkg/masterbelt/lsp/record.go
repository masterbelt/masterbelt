package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// The editor side of record literals: which record type a literal's fields
// fill (the typed form names it, the inferred form takes it from the checking
// context), the field-name completion inside a literal, and the hover cards
// for a field initializer and the typed form's type name.

// recordTypes maps every record literal in the file to the type whose record
// its fields fill: the literal's own named type for the typed form, the type
// the checking context pushes down for the inferred form — mirroring the
// checker's push-down through annotations, record fields, collection
// elements, and method result types.
func recordTypes(doc view) map[*ast.RecordLit]ir.Type {
	out := map[*ast.RecordLit]ir.Type{}
	var push func(e ast.Expr, want ir.Type)
	push = func(e ast.Expr, want ir.Type) {
		switch e := e.(type) {
		case *ast.RecordLit:
			t := want
			if e.TypeName != "" {
				t = nil
				if def := findTypeDef(doc.TypeNames(), e.TypeName); def != nil && !def.Builtin {
					t = &ir.Named{Def: def}
				}
			}
			if t == nil || t == ir.Invalid {
				return
			}
			rec, ok := recordOf(t)
			if !ok {
				return
			}
			out[e] = t
			declared := make(map[string]ir.Type, len(rec.Fields))
			for _, f := range rec.Fields {
				declared[f.Name] = f.Type
			}
			for _, f := range e.Fields {
				if ft, ok := declared[f.Name]; ok && f.Value != nil {
					push(f.Value, ft)
				}
			}
		case *ast.CollectionLit:
			app, ok := want.(*ir.App)
			if !ok || app.Def == nil {
				return
			}
			for _, entry := range e.Entries {
				switch {
				case len(app.Args) == 1 && entry.Value != nil:
					push(entry.Value, app.Args[0])
				case len(app.Args) == 2:
					if entry.Key != nil {
						push(entry.Key, app.Args[0])
					}
					if entry.Value != nil {
						push(entry.Value, app.Args[1])
					}
				}
			}
		}
	}
	for _, c := range doc.Module().Consts {
		if c.Syntax == nil || c.Syntax.Value == nil {
			continue
		}
		push(c.Syntax.Value, c.Type)
	}
	for _, def := range doc.Module().Types {
		self := ir.Type(&ir.Named{Def: def})
		for _, m := range def.Methods {
			if m.Syntax == nil {
				continue
			}
			want := m.Result
			if _, isSelf := want.(*ir.SelfType); isSelf {
				want = self
			}
			for _, stmt := range m.Syntax.Body {
				if ret, ok := stmt.(*ast.ReturnStmt); ok && ret.Value != nil {
					push(ret.Value, want)
				}
			}
		}
	}
	return out
}

// recordLitOf returns the AST record literal backing a RecordLit CST node.
func recordLitOf(doc view, node *cst.Node) *ast.RecordLit {
	var lit *ast.RecordLit
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if l, ok := e.(*ast.RecordLit); ok && l.Syntax() == node {
			lit = l
		}
	})
	return lit
}

// recordFieldContextAt reports the record literal whose field-name position
// offset sits in: directly in the literal's field block (between fields, on a
// brace or separator), or on a field's name before its colon. A position
// inside a field's value belongs to the value namespace, and the typed form's
// leading type name to the type namespace; neither reports here.
func recordFieldContextAt(root cst.Tree, offset int) (*cst.Node, bool) {
	var lit cst.Tree
	found := false
	node := root
	for {
		if k, ok := node.Kind(); ok && k == cst.RecordLit {
			lit, found = node, true
		}
		child, ok := childContaining(node, offset)
		if !ok {
			break
		}
		node = child
	}
	if !found {
		return nil, false
	}
	child, ok := childContaining(lit, offset)
	if !ok {
		return nil, false
	}
	if k, isNode := child.Kind(); isNode {
		if k != cst.RecordField {
			return nil, false
		}
		// Inside a field: the name position sits before the ":".
		for _, fc := range child.Children() {
			if tk, isTok := fc.TokenKind(); isTok && tk == token.Colon {
				if offset > fc.Offset() {
					return nil, false // in the field's value
				}
				break
			}
		}
		green, _ := lit.Node()
		return green, true
	}
	if tk, _ := child.TokenKind(); tk == token.Ident {
		return nil, false // the typed form's type name
	}
	green, _ := lit.Node()
	return green, true
}

// recordFieldItems returns the field-name candidates inside the record
// literal at offset: the declared fields its record type still leaves
// uninitialized, each detailed with its type. ok reports that the cursor
// claims a field-name position — even when the record type is unknown, where
// no candidates beat the value namespace.
func recordFieldItems(doc view, offset int) ([]protocol.CompletionItem, bool) {
	root := doc.AST().Concrete().Tree()
	litNode, ok := recordFieldContextAt(root, offset)
	if !ok && offset > 0 {
		litNode, ok = recordFieldContextAt(root, offset-1)
	}
	if !ok {
		return nil, false
	}
	lit := recordLitOf(doc, litNode)
	if lit == nil {
		return nil, true
	}
	t, ok := recordTypes(doc)[lit]
	if !ok {
		return nil, true
	}
	rec, ok := recordOf(t)
	if !ok {
		return nil, true
	}

	written := make(map[string]bool, len(lit.Fields))
	for _, f := range lit.Fields {
		written[f.Name] = true
	}
	// The partial name being typed parses as a field of its own; offering its
	// completion means not counting it as written.
	if name, ok := recordFieldNameAt(root, doc.Buffer(), offset); ok {
		delete(written, name)
	}

	kind := protocol.CompletionItemKindField
	items := make([]protocol.CompletionItem, 0, len(rec.Fields))
	for _, f := range rec.Fields {
		if written[f.Name] {
			continue
		}
		items = append(items, protocol.CompletionItem{
			Label:  f.Name,
			Kind:   &kind,
			Detail: ": " + f.Type.String(),
		})
	}
	return items, true
}

// recordFieldNameAt returns the field name whose identifier token covers
// offset (probing one byte back, since the cursor usually sits just past the
// partial name being typed).
func recordFieldNameAt(root cst.Tree, buf source.Buffer, offset int) (string, bool) {
	at := func(off int) (string, bool) {
		leaf, parent, ok := leafAt(root, off)
		if !ok {
			return "", false
		}
		if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
			return "", false
		}
		if pk, isNode := parent.Kind(); !isNode || pk != cst.RecordField {
			return "", false
		}
		return leaf.Text(buf), true
	}
	if name, ok := at(offset); ok {
		return name, true
	}
	if offset > 0 {
		return at(offset - 1)
	}
	return "", false
}

// recordFieldHover describes the field initializer at offset — the cursor on
// its name inside a record literal — as the declared field it fills: its name
// and type, exactly the card a field access shows.
func recordFieldHover(doc view, offset int) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	pk, isNode := parent.Kind()
	if !isNode {
		return nil
	}
	buf := doc.Buffer()
	name := leaf.Text(buf)

	switch pk {
	case cst.RecordField:
		// The field's record literal is the node enclosing the field.
		litNode, ok := enclosingRecordLit(doc.AST().Concrete().Tree(), leaf.Offset())
		if !ok {
			return nil
		}
		lit := recordLitOf(doc, litNode)
		if lit == nil {
			return nil
		}
		t, ok := recordTypes(doc)[lit]
		if !ok {
			return nil
		}
		f, ok := fieldOf(t, name)
		if !ok {
			return nil
		}
		r := toRange(buf, leaf.Offset(), leaf.End())
		return &protocol.Hover{
			Contents: protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: "```masterbelt\n" + f.Name + ": " + f.Type.String() + "\n```",
			},
			Range: &r,
		}
	case cst.RecordLit:
		// The typed form's type name hovers as the type it names.
		if t := findTypeDef(doc.TypeNames(), name); t != nil {
			return typeHover(t, buf, leaf)
		}
	}
	return nil
}

// enclosingRecordLit returns the innermost RecordLit CST node containing
// offset.
func enclosingRecordLit(root cst.Tree, offset int) (*cst.Node, bool) {
	var lit cst.Tree
	found := false
	node := root
	for {
		if k, ok := node.Kind(); ok && k == cst.RecordLit {
			lit, found = node, true
		}
		child, ok := childContaining(node, offset)
		if !ok {
			break
		}
		node = child
	}
	if !found {
		return nil, false
	}
	green, _ := lit.Node()
	return green, true
}
