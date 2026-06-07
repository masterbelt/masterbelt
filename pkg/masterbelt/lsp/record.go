package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
	for _, c := range doc.Module().Consts {
		if c.Syntax == nil || c.Syntax.Value == nil {
			continue
		}
		pushRecordType(doc, out, c.Syntax.Value, c.Type)
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
			pushRecordBody(doc, out, m.Syntax.Body, want)
		}
	}
	for _, fn := range doc.Module().Funcs {
		if fn.Syntax == nil {
			continue
		}
		pushRecordBody(doc, out, fn.Syntax.Body, fn.Result)
	}
	return out
}

// pushRecordType pushes the expected type want down into an expression: a
// record literal records its own resolved type and recurses into its declared
// fields; a collection literal recurses into its entries against the element
// (and key) type. It mirrors the checker's push-down through record fields and
// collection elements, recording every reached literal in out.
func pushRecordType(doc view, out map[*ast.RecordLit]ir.Type, e ast.Expr, want ir.Type) {
	switch e := e.(type) {
	case *ast.RecordLit:
		pushRecordLit(doc, out, e, want)
	case *ast.CollectionLit:
		pushRecordCollection(doc, out, e, want)
	}
}

// pushRecordLit records the type a record literal's fields fill — its own
// named type for the typed form, the pushed-down want for the inferred form —
// then recurses into each field's value against the declared field type.
func pushRecordLit(doc view, out map[*ast.RecordLit]ir.Type, e *ast.RecordLit, want ir.Type) {
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
			pushRecordType(doc, out, f.Value, ft)
		}
	}
}

// pushRecordCollection recurses into a collection literal's entries against
// the element type (single-arg form) or the key and value types (two-arg
// form) of the expected collection type.
func pushRecordCollection(doc view, out map[*ast.RecordLit]ir.Type, e *ast.CollectionLit, want ir.Type) {
	app, ok := want.(*ir.App)
	if !ok || app.Def == nil {
		return
	}
	for _, entry := range e.Entries {
		switch {
		case len(app.Args) == 1 && entry.Value != nil:
			pushRecordType(doc, out, entry.Value, app.Args[0])
		case len(app.Args) == 2:
			if entry.Key != nil {
				pushRecordType(doc, out, entry.Key, app.Args[0])
			}
			if entry.Value != nil {
				pushRecordType(doc, out, entry.Value, app.Args[1])
			}
		}
	}
}

// pushRecordBody pushes the expected types through a method or function body:
// the declared result type to every return value (reached through the if/switch
// control flow, not only the top level), and a let's annotated record type to
// its initializer — so an inferred record literal returned from inside a
// branch, or bound by `let p: Point = { ... }`, gets its field typing the same
// way a top-level return does.
func pushRecordBody(doc view, out map[*ast.RecordLit]ir.Type, body []ast.Stmt, result ir.Type) {
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value != nil {
				pushRecordType(doc, out, stmt.Value, result)
			}
		case *ast.LetStmt:
			if stmt.Value != nil && stmt.Type != nil {
				if t := annotationType(doc, stmt.Type); t != nil {
					pushRecordType(doc, out, stmt.Value, t)
				}
			}
		case *ast.SwitchStmt:
			pushRecordSwitch(doc, out, stmt, result)
		case *ast.MatchStmt:
			pushRecordMatch(doc, out, stmt, result)
		case *ast.IfStmt:
			pushRecordIf(doc, out, stmt, result)
		case *ast.ForStmt:
			// A return inside the loop body still yields the function's result,
			// so a record literal returned from a for needs its field typing the
			// same way a returned-from-a-branch one does.
			pushRecordBody(doc, out, stmt.Body, result)
		case *ast.ExprStmt, *ast.AssignStmt:
			// Neither carries a result-typed slot for a record literal: a
			// bare expression has no expected type to push, and an
			// assignment's target is a let local, not a return position.
			// Listed so a new statement kind hits the default.
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
}

// pushRecordSwitch descends every arm body, the else body, and the after-else
// arm bodies of a switch, pushing the result type through each.
func pushRecordSwitch(doc view, out map[*ast.RecordLit]ir.Type, s *ast.SwitchStmt, result ir.Type) {
	for _, arm := range s.Arms {
		pushRecordBody(doc, out, arm.Body, result)
	}
	pushRecordBody(doc, out, s.Else, result)
	for _, arm := range s.AfterElse {
		pushRecordBody(doc, out, arm.Body, result)
	}
}

// pushRecordMatch descends every arm body, the else body, and the after-else
// arm bodies of a match, pushing the result type through each.
func pushRecordMatch(doc view, out map[*ast.RecordLit]ir.Type, s *ast.MatchStmt, result ir.Type) {
	for _, arm := range s.Arms {
		pushRecordBody(doc, out, arm.Body, result)
	}
	pushRecordBody(doc, out, s.Else, result)
	for _, arm := range s.AfterElse {
		pushRecordBody(doc, out, arm.Body, result)
	}
}

// pushRecordIf descends an if's then body, its else-if chain, and its else
// body, pushing the result type through each — the statement-body twin of
// letTypeOfIf.
func pushRecordIf(doc view, out map[*ast.RecordLit]ir.Type, s *ast.IfStmt, result ir.Type) {
	pushRecordBody(doc, out, s.Then, result)
	if s.ElseIf != nil {
		pushRecordIf(doc, out, s.ElseIf, result)
	}
	pushRecordBody(doc, out, s.Else, result)
}

// annotationType resolves a let binding's record-relevant type annotation to a
// resolved type: a plain (or namespace-qualified) name of a non-builtin record
// type. It is deliberately narrow — only the form a record literal can fill —
// so a let with an annotated record type pushes its field typing down, without
// reimplementing the checker's full annotation resolution.
func annotationType(doc view, te ast.TypeExpr) ir.Type {
	named, ok := te.(*ast.NamedType)
	if !ok || named.Name == "" || len(named.Args) > 0 {
		return nil
	}
	var def *ir.TypeDef
	if named.Namespace != "" {
		def = findTypeDef(doc.QualifiedTypeNames()[named.Namespace], named.Name)
	} else {
		def = findTypeDef(doc.TypeNames(), named.Name)
	}
	if def == nil || def.Builtin {
		return nil
	}
	return &ir.Named{Def: def}
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
	default:
		// Any other parent kind is not a record field initializer or a typed
		// literal's name: there is no field card to show.
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
