package lsp

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
	if items, ok := recordFieldItems(doc, offset); ok {
		return &protocol.CompletionList{Items: items}
	}
	if enumBaseTypeAt(root, offset) {
		return &protocol.CompletionList{Items: enumBaseTypeItems(doc)}
	}
	if typeContextAt(root, offset) {
		return &protocol.CompletionList{Items: typeItems(doc)}
	}
	effectful := effectfulContextAt(root, offset)
	// A value position whose expected type is an enum offers that enum's bare
	// members alongside the value namespace — the position admits a constant or a
	// call too, so the members are added, not offered exclusively (unlike the
	// member-after-dot completion, which claims its position). They lead so they
	// sort first.
	items := expectedEnumItems(doc, root, offset)
	items = append(items, constantItems(doc)...)
	items = append(items, functionItems(doc, effectful)...)
	items = append(items, constructorItems(doc)...)
	items = append(items, valueKeywordItems(effectful)...)
	return &protocol.CompletionList{Items: items}
}

// effectfulContextAt reports whether offset sits inside a function or method
// declaration's body — the positions whose declared effects admit effectful
// calls. Everywhere else (a constant initializer, an assert condition, a
// where clause) is evaluated at compile time and must be pure, so effectful
// completions are suppressed there.
func effectfulContextAt(root cst.Tree, offset int) bool {
	t := root
	for {
		if node, ok := t.Node(); ok {
			switch node.Kind() {
			case cst.FuncDecl, cst.MethodDecl:
				return true
			default:
				// Any other kind is not an effectful body boundary: keep
				// descending toward offset before concluding it is pure.
			}
		}
		child, ok := childContaining(t, offset)
		if !ok {
			return false
		}
		t = child
	}
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
	// A member access whose receiver names a type (Rarity., int8., Level.)
	// offers the type's members — enum members and associated constants — each
	// labelled with its value.
	if items, ok := typeMemberItems(doc, member); ok {
		return items, true
	}
	recv := receiverTypeOf(doc, member.Receiver, doc.Trees(), offset)
	if recv == nil || recv == ir.Invalid {
		return nil, true
	}

	var items []protocol.CompletionItem
	if methods, subst, ok := doc.ReceiverMethods(recv); ok {
		methodKind := protocol.CompletionItemKindMethod
		propertyKind := protocol.CompletionItemKindProperty
		snippet := protocol.InsertTextFormatSnippet
		for _, m := range methods {
			switch m.Kind {
			case ir.MethodSetter, ir.MethodStatic:
				// A setter is written on the left of an assignment, not read after a
				// dot; a static fn is reached through the type (Type.name), not a
				// value. Neither belongs in a value member-access completion.
				continue
			case ir.MethodGetter:
				// A getter reads as a property (value.name), so it is offered as a
				// Property with its result type, not a call snippet.
				item := protocol.CompletionItem{
					Label:  m.Name,
					Kind:   &propertyKind,
					Detail: ": " + types.Substitute(m.Result, subst).String(),
				}
				if len(m.Doc) > 0 {
					item.Documentation = &protocol.MarkupContent{Kind: protocol.Markdown, Value: strings.Join(m.Doc, "\n")}
				}
				items = append(items, item)
			default:
				item := protocol.CompletionItem{
					Label:  m.Name,
					Kind:   &methodKind,
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

// typeMemberItems returns the completion items for a type member access
// (Rarity., int8., Level.): one item per enum member (labelled with its base
// value) and one per associated constant (labelled with its value), and reports
// whether the receiver names a type (so the caller offers members only). A
// receiver that is not a type — a value, or an unknown name — is left to the
// value-method path.
func typeMemberItems(doc view, member *ast.MemberExpr) ([]protocol.CompletionItem, bool) {
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok {
		return nil, false
	}
	// A local value shadowing the type name means this is a value access, not a
	// type access.
	if doc.Resolve(recv) != nil {
		return nil, false
	}
	def := lookupTypeName(doc, recv.Name)
	if def == nil || (def.Enum == nil && len(def.Consts) == 0 && !hasStatic(def)) {
		return nil, false
	}
	var items []protocol.CompletionItem
	if def.Enum != nil {
		kind := protocol.CompletionItemKindEnumMember
		for _, m := range def.Enum.Members {
			items = append(items, protocol.CompletionItem{
				Label:  m.Name,
				Kind:   &kind,
				Detail: "= " + m.Value.String(),
			})
		}
	}
	kind := protocol.CompletionItemKindConstant
	for _, c := range def.Consts {
		detail := ": " + c.Type.String()
		if c.Value != nil {
			detail = "= " + c.Value.String()
		}
		item := protocol.CompletionItem{Label: c.Name, Kind: &kind, Detail: detail}
		if len(c.Doc) > 0 {
			item.Documentation = &protocol.MarkupContent{Kind: protocol.Markdown, Value: strings.Join(c.Doc, "\n")}
		}
		items = append(items, item)
	}
	// Static fns are reached through the type (Type.name(...)) — the same Type.Name
	// path the enum members and associated constants above take — so they join the
	// type-member completion as Function items with a call snippet.
	fnKind := protocol.CompletionItemKindFunction
	snippet := protocol.InsertTextFormatSnippet
	for _, m := range def.Methods {
		if m.Kind != ir.MethodStatic {
			continue
		}
		item := protocol.CompletionItem{Label: m.Name, Kind: &fnKind, Detail: methodSignatureSubst(m, nil)}
		if len(m.Doc) > 0 {
			item.Documentation = &protocol.MarkupContent{Kind: protocol.Markdown, Value: strings.Join(m.Doc, "\n")}
		}
		item.InsertText = callSnippet(m, nil)
		item.InsertTextFormat = &snippet
		items = append(items, item)
	}
	return items, true
}

// hasStatic reports whether a type definition declares any static fn — so a
// type-member completion (Type.) is offered even when the type has no enum
// members or associated constants.
func hasStatic(def *ir.TypeDef) bool {
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic {
			return true
		}
	}
	return false
}

// lookupEnumType returns the enum definition named name in the document's type
// scope, or nil when no enum has that name.
func lookupEnumType(doc view, name string) *ir.TypeDef {
	def := lookupTypeName(doc, name)
	if def == nil || def.Enum == nil {
		return nil
	}
	return def
}

// lookupTypeName returns the type definition named name in the document's type
// scope, or nil when no type has that name. It covers any type, so an
// associated-const owner (int8, Level) is found, not just enums.
func lookupTypeName(doc view, name string) *ir.TypeDef {
	for _, def := range doc.TypeNames() {
		if def.Name == name {
			return def
		}
	}
	return nil
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
	appendParamsSnippet(&b, m.Params, subst)
	b.WriteString(")")
	return b.String()
}

// appendParamsSnippet writes the snippet text of a call's arguments — a tab
// stop per parameter, a function-typed one expanded to a fn literal — shared
// by the method and function call snippets.
func appendParamsSnippet(b *strings.Builder, params []ir.Param, subst map[string]ir.Type) {
	stop := 0
	nextStop := func() int { stop++; return stop }
	for i, p := range params {
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
				fmt.Fprintf(b, "${%d:%s}", nextStop(), paramName(j))
				if isConcrete(ft) {
					b.WriteString(": " + snippetEscape(ft.String()))
				}
			}
			fmt.Fprintf(b, ") -> ${%d}", nextStop())
			continue
		}
		fmt.Fprintf(b, "${%d:%s}", nextStop(), p.Name)
	}
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

// enumBaseTypeNames is the set of types an enum may use as its base — the
// integer-family primitives and string. It is the same set the semantic
// analyzer accepts (invalid_enum_base_type rejects anything else), kept here so
// completion never offers a base type the analyzer would reject.
var enumBaseTypeNames = []string{
	"nint", "sbyte", "short", "int", "long",
	"nuint", "byte", "ushort", "uint", "ulong",
	"string",
}

// enumBaseTypeItems is the completion items for an enum's base-type position:
// one item per legal base type (the integer family and string), drawn from the
// document's type scope so each carries its real definition's doc and kind. A
// base name absent from scope — impossible for the always-present builtins —
// is simply skipped.
func enumBaseTypeItems(doc view) []protocol.CompletionItem {
	byName := map[string]*ir.TypeDef{}
	for _, t := range doc.TypeNames() {
		if _, seen := byName[t.Name]; !seen {
			byName[t.Name] = t
		}
	}
	items := make([]protocol.CompletionItem, 0, len(enumBaseTypeNames))
	for _, name := range enumBaseTypeNames {
		if t, ok := byName[name]; ok {
			items = append(items, typeItem(name, t))
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

// constructorItems is one completion item per type in scope whose conversion
// form constructs a value (the error and range types): a call snippet with the
// arguments ready to fill in. Each constructor carries its own signature and
// snippet — error("message"), range(start, end) — so the completion offers the
// right shape per type rather than the error form for all.
func constructorItems(doc view) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindConstructor
	snippet := protocol.InsertTextFormatSnippet
	items := make([]protocol.CompletionItem, 0, len(doc.Constructors()))
	for _, t := range doc.Constructors() {
		detail, insert := constructorSignature(t.Name)
		item := protocol.CompletionItem{
			Label:  t.Name,
			Kind:   &kind,
			Detail: detail,
		}
		if len(t.Doc) > 0 {
			item.Documentation = &protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: strings.Join(t.Doc, "\n"),
			}
		}
		item.InsertText = insert
		item.InsertTextFormat = &snippet
		items = append(items, item)
	}
	return items
}

// constructorSignature returns the detail label and the snippet insert text for
// a value-constructing builtin: error("message") takes one string, range(start,
// end) two ints (the optional third step argument is left out of the snippet —
// the two-argument form is the common case; the literal a..b and the explicit
// range(s, e, step) cover the rest). An unrecognized constructor falls back to a
// bare call snippet with one placeholder, so a future constructor still completes
// sensibly until it is given its own shape here.
func constructorSignature(name string) (detail, insert string) {
	switch name {
	case "range":
		return "range(start: nint, end: nint)", "range(${1:start}, ${2:end})"
	case "error":
		return "error(message: string)", "error(\"${1:message}\")"
	default:
		return name + "(value)", name + "(${1:value})"
	}
}

// valueKeywordItems is the keywords that may begin a value: the literals
// true/false/null, and fn, which starts a function literal (the value form of a
// function type, e.g. the argument to list.map). In an effectful context — a
// function or method body — await is offered too; a pure position cannot
// suspend, so it is suppressed there.
func valueKeywordItems(effectful bool) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindKeyword
	words := []string{"false", "fn", "null", "true"}
	if effectful {
		words = append(words, "await")
	}
	items := make([]protocol.CompletionItem, 0, len(words))
	for _, w := range words {
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

// enumBaseTypeAt reports whether offset sits in an enum's base-type position
// (enum E: <here> { ... }) — the type clause that immediately follows the enum
// name. Like typeContextAt it probes one byte back as well, since the cursor
// usually sits just past the partial word being typed. An enum base may only be
// an integer-family or string primitive, so this position offers a restricted
// type set, not the general one.
func enumBaseTypeAt(root cst.Tree, offset int) bool {
	if inEnumBaseType(root, offset) {
		return true
	}
	return offset > 0 && inEnumBaseType(root, offset-1)
}

// inEnumBaseType descends to the leaf at offset and reports whether the path
// passes through a TypeClause whose parent is an EnumDecl — the enum's
// base-type annotation, the one type clause an enum carries.
func inEnumBaseType(root cst.Tree, offset int) bool {
	node := root
	parentKind := cst.File
	for {
		if kind, ok := node.Kind(); ok && kind == cst.TypeClause && parentKind == cst.EnumDecl {
			return true
		}
		child, found := childContaining(node, offset)
		if !found {
			return false
		}
		if kind, ok := node.Kind(); ok {
			parentKind = kind
		}
		node = child
	}
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
		cst.RecordLit, cst.RecordField, cst.ParenExpr, cst.AssertDecl, cst.WhereClause:
		// A record literal is a value context; its field-name positions are
		// claimed by recordFieldItems before classification matters.
		return false, true
	default:
		return false, false
	}
}
