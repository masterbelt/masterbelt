package lsp

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// Completion offers the program's value namespace. masterbelt's nameable values
// are its constants, plus the value keywords true/false/null and fn (which
// begins a function literal), so in a value position those are the candidates; a
// type position (an annotation, union, generic argument, record field, or
// parameter type) yields the in-scope type names instead.
//
// The candidate set is the whole namespace, not a prefix-filtered slice: the
// editor filters the list against the characters already typed, so re-querying
// on each keystroke is unnecessary and IsIncomplete stays false.

// completion returns the completion candidates at offset: the project's files
// inside a use declaration's path string, the receiver's methods and fields
// after a member dot, type names in a type position, and the value namespace
// (constants plus the value literals) otherwise.
func completion(doc view, offset int) *protocol.CompletionList {
	root := doc.AST().Concrete().Tree()
	if inUsePath(root, offset) {
		return &protocol.CompletionList{Items: usePathItems(doc)}
	}
	if items, ok := memberItems(doc, offset); ok {
		return &protocol.CompletionList{Items: items}
	}
	if typeContextAt(root, offset) {
		return &protocol.CompletionList{Items: typeItems(doc)}
	}
	items := constantItems(doc)
	items = append(items, valueKeywordItems()...)
	return &protocol.CompletionList{Items: items}
}

// memberItems returns the candidates of a member access at offset — the
// receiver type's methods and record fields — and reports whether the cursor
// sits in one (a claimed position offers members only, never the value
// namespace). A method inserts as a call snippet; a function-typed parameter
// expands to a fn literal with the receiver's solved types annotated, ready
// to fill in.
func memberItems(doc view, offset int) ([]protocol.CompletionItem, bool) {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil, false
	}
	recv := receiverTypeOf(doc, member.Receiver, doc.Trees(), offset)
	if recv == nil || recv == ir.Invalid {
		return nil, true
	}

	var items []protocol.CompletionItem
	if methods, subst, ok := doc.ReceiverMethods(recv); ok {
		kind := protocol.CompletionItemKindMethod
		snippet := protocol.InsertTextFormatSnippet
		for _, m := range methods {
			item := protocol.CompletionItem{
				Label:  m.Name,
				Kind:   &kind,
				Detail: methodSignatureSubst(m, subst),
			}
			if len(m.Doc) > 0 {
				item.Documentation = &protocol.MarkupContent{
					Kind:  protocol.Markdown,
					Value: strings.Join(m.Doc, "\n"),
				}
			}
			item.InsertText = callSnippet(m, subst)
			item.InsertTextFormat = &snippet
			items = append(items, item)
		}
	}
	if rec, ok := recordOf(recv); ok {
		kind := protocol.CompletionItemKindField
		for _, f := range rec.Fields {
			items = append(items, protocol.CompletionItem{
				Label:  f.Name,
				Kind:   &kind,
				Detail: ": " + f.Type.String(),
			})
		}
	}
	return items, true
}

// memberAccessAt finds the member access whose dot or member name the cursor
// sits at (probing one byte back as well, since the cursor usually sits just
// past the dot or the partial name being typed).
func memberAccessAt(doc view, offset int) (*ast.MemberExpr, bool) {
	node, ok := memberNodeAt(doc, offset)
	if !ok && offset > 0 {
		node, ok = memberNodeAt(doc, offset-1)
	}
	if !ok {
		return nil, false
	}
	var member *ast.MemberExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if m, ok := e.(*ast.MemberExpr); ok && m.Syntax() == node {
			member = m
		}
	})
	return member, member != nil
}

// memberNodeAt returns the MemberExpr CST node whose dot or member-name token
// covers offset.
func memberNodeAt(doc view, offset int) (*cst.Node, bool) {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil, false
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MemberExpr {
		return nil, false
	}
	if k, isTok := leaf.TokenKind(); !isTok || (k != token.Ident && k != token.Dot) {
		return nil, false
	}
	node, _ := parent.Node()
	return node, true
}

// recordOf returns the record body behind a type — directly, or through a
// named type's definition.
func recordOf(t ir.Type) (*ir.Record, bool) {
	switch t := t.(type) {
	case *ir.Record:
		return t, true
	case *ir.Named:
		if t.Def != nil {
			return recordOf(t.Def.Body)
		}
	}
	return nil, false
}

// callSnippet renders a method as an insertable call: each parameter a tab
// stop, and a function-typed parameter expanded to a fn literal — its
// parameters named and annotated where the receiver solved them concretely,
// left to inference otherwise — with a tab stop for the arrow body (the
// expression idiom; typing "{" instead opens a block for statement bodies):
//
//	map(fn(${1:x}: int8) -> ${2})
func callSnippet(m *ir.Method, subst map[string]ir.Type) string {
	var b strings.Builder
	b.WriteString(m.Name)
	b.WriteString("(")
	stop := 0
	nextStop := func() int { stop++; return stop }
	for i, p := range m.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		pt := types.Substitute(p.Type, subst)
		if f, ok := pt.(*ir.Func); ok {
			b.WriteString("fn(")
			for j, ft := range f.Params {
				if j > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "${%d:%s}", nextStop(), paramName(j))
				if isConcrete(ft) {
					b.WriteString(": " + snippetEscape(ft.String()))
				}
			}
			fmt.Fprintf(&b, ") -> ${%d}", nextStop())
			continue
		}
		fmt.Fprintf(&b, "${%d:%s}", nextStop(), p.Name)
	}
	b.WriteString(")")
	return b.String()
}

// paramName names the j-th generated fn-literal parameter: x, y, z, then x3…
func paramName(j int) string {
	if j < 3 {
		return string(rune('x' + j))
	}
	return fmt.Sprintf("x%d", j)
}

// isConcrete reports whether a type holds no type variable and no self type —
// the condition for writing it as an annotation in generated code (anything
// else is left to inference).
func isConcrete(t ir.Type) bool {
	switch t := t.(type) {
	case *ir.TypeVar, *ir.SelfType:
		return false
	case *ir.App:
		for _, a := range t.Args {
			if !isConcrete(a) {
				return false
			}
		}
	case *ir.Func:
		for _, p := range t.Params {
			if !isConcrete(p) {
				return false
			}
		}
		return isConcrete(t.Result)
	case *ir.Union:
		for _, m := range t.Members {
			if !isConcrete(m) {
				return false
			}
		}
	case *ir.Record:
		for _, f := range t.Fields {
			if !isConcrete(f.Type) {
				return false
			}
		}
	}
	return t != ir.Invalid
}

// snippetEscape escapes the characters the snippet syntax owns.
func snippetEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `$`, `\$`, `}`, `\}`)
	return r.Replace(s)
}

// inUsePath reports whether offset sits inside the path string of a use
// declaration — where completion offers the project's files instead of value
// or type names. Like typeContextAt it probes one byte back as well, since the
// cursor usually sits just past the characters being typed.
func inUsePath(root cst.Tree, offset int) bool {
	if onUseString(root, offset) {
		return true
	}
	return offset > 0 && onUseString(root, offset-1)
}

// onUseString descends to the leaf at offset and reports whether it is the
// String token of a use declaration.
func onUseString(root cst.Tree, offset int) bool {
	node := root
	inUse := false
	for {
		if kind, ok := node.Kind(); ok && kind == cst.UseDecl {
			inUse = true
		}
		child, found := childContaining(node, offset)
		if !found {
			tok, ok := node.Token()
			return ok && tok.Kind() == token.String && inUse
		}
		node = child
	}
}

// usePathItems lists every use path the importing file could write — the
// project layer's CandidateImports, the verified inverse of its resolution
// rule, so the editor can never offer a path the project would reject.
// Outside a project there are no siblings to offer.
func usePathItems(doc view) []protocol.CompletionItem {
	ws := doc.ws
	if ws.proj == nil {
		return nil
	}
	kind := protocol.CompletionItemKindFile
	candidates := ws.proj.CandidateImports(project.FileID(doc.id))
	items := make([]protocol.CompletionItem, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, protocol.CompletionItem{Label: c, Kind: &kind})
	}
	return items
}

// typeItems is one completion item per in-scope type name: the file's own types
// (kind Class), the builtin/prelude primitives (kind Struct), and the
// namespace-qualified names of the file's namespace imports (geo.Point), each
// documented with its doc comment when it has one.
func typeItems(doc view) []protocol.CompletionItem {
	items := make([]protocol.CompletionItem, 0)
	for _, t := range doc.TypeNames() {
		if t.Name == "" {
			continue
		}
		items = append(items, typeItem(t.Name, t))
	}
	qualified := doc.QualifiedTypeNames()
	for _, ns := range slices.Sorted(maps.Keys(qualified)) {
		for _, t := range qualified[ns] {
			items = append(items, typeItem(ns+"."+t.Name, t))
		}
	}
	return items
}

// typeItem renders one type definition as a completion item under the given
// label (its plain or namespace-qualified name).
func typeItem(label string, t *ir.TypeDef) protocol.CompletionItem {
	kind := protocol.CompletionItemKindClass
	if t.Builtin {
		kind = protocol.CompletionItemKindStruct
	}
	item := protocol.CompletionItem{Label: label, Kind: &kind}
	if len(t.Doc) > 0 {
		item.Documentation = &protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: strings.Join(t.Doc, "\n"),
		}
	}
	return item
}

// constantItems is one completion item per declared constant, labelled with the
// name, detailed with the inferred type, and documented with the doc comment.
// Duplicate declarations of a name contribute a single item.
func constantItems(doc view) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindConstant
	var items []protocol.CompletionItem
	seen := map[string]bool{}
	for _, c := range doc.Module().Consts {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true

		item := protocol.CompletionItem{Label: c.Name, Kind: &kind}
		if c.Type != ir.Invalid {
			item.Detail = ": " + c.Type.String()
		}
		if len(c.Doc) > 0 {
			item.Documentation = &protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: strings.Join(c.Doc, "\n"),
			}
		}
		items = append(items, item)
	}
	return items
}

// valueKeywordItems is the keywords that may begin a value: the literals
// true/false/null, and fn, which starts a function literal (the value form of a
// function type, e.g. the argument to list.map).
func valueKeywordItems() []protocol.CompletionItem {
	kind := protocol.CompletionItemKindKeyword
	items := make([]protocol.CompletionItem, 0, 4)
	for _, w := range []string{"false", "fn", "null", "true"} {
		items = append(items, protocol.CompletionItem{Label: w, Kind: &kind})
	}
	return items
}

// typeContextAt reports whether offset sits in a type position. It descends the
// concrete tree to the innermost node spanning the cursor — probing one byte
// back as well, since the cursor usually sits just past the partial word being
// typed — and takes the classification of the innermost node that is
// unambiguously a value or a type context. A position that is neither (e.g. a
// fresh declaration name) is treated as a value context, since that is where
// completion is overwhelmingly invoked.
func typeContextAt(root cst.Tree, offset int) bool {
	if isType, ok := classifyContext(root, offset); ok {
		return isType
	}
	if offset > 0 {
		if isType, ok := classifyContext(root, offset-1); ok {
			return isType
		}
	}
	return false
}

// classifyContext descends from root into the child spanning offset, returning
// the value/type classification of the innermost node that has one (ok is false
// when no node on the path is classifiable).
func classifyContext(root cst.Tree, offset int) (isType, ok bool) {
	node := root
	for {
		if t, set := contextKind(node); set {
			isType, ok = t, true
		}
		child, found := childContaining(node, offset)
		if !found {
			return isType, ok
		}
		node = child
	}
}

// childContaining returns the child of t whose byte span contains offset.
func childContaining(t cst.Tree, offset int) (cst.Tree, bool) {
	for _, c := range t.Children() {
		if c.Offset() <= offset && offset < c.End() {
			return c, true
		}
	}
	return cst.Tree{}, false
}

// contextKind classifies a node as a type context or a value context, or reports
// set=false for a leaf or a node that is neither (a declaration scaffold like
// File/ConstDecl, which leaves the classification to a deeper node).
func contextKind(t cst.Tree) (isType, set bool) {
	kind, ok := t.Kind()
	if !ok {
		return false, false // a leaf token
	}
	switch kind {
	case cst.TypeClause, cst.TypeName, cst.UnionType, cst.GenericArgs, cst.GenericParams,
		cst.GenericParam, cst.RecordType, cst.Field, cst.FuncType, cst.Param, cst.BuiltinType:
		return true, true
	case cst.Initializer, cst.BinaryExpr, cst.UnaryExpr, cst.MemberExpr, cst.CallExpr,
		cst.NameRef, cst.Literal, cst.SelfExpr, cst.CollectionLit, cst.MapEntry, cst.ReturnStmt,
		cst.ParenExpr, cst.AssertDecl, cst.WhereClause:
		return false, true
	default:
		return false, false
	}
}
