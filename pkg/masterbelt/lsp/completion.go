package lsp

import (
	"maps"
	"slices"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/project"
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
// inside a use declaration's path string, type names in a type position, and
// the value namespace (constants plus the value literals) otherwise.
func completion(doc view, offset int) *protocol.CompletionList {
	root := doc.AST().Concrete().Tree()
	if inUsePath(root, offset) {
		return &protocol.CompletionList{Items: usePathItems(doc)}
	}
	if typeContextAt(root, offset) {
		return &protocol.CompletionList{Items: typeItems(doc)}
	}
	items := constantItems(doc)
	items = append(items, valueKeywordItems()...)
	return &protocol.CompletionList{Items: items}
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
		cst.ParenExpr, cst.AssertDecl:
		return false, true
	default:
		return false, false
	}
}
