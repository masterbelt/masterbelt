package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// expectedEnumItems offers a bare enum member (Common rather than Rarity.Common)
// at the two value positions masterbelt's lowering resolves one through: a
// top-level const initializer annotated with an enum (the const binder folds the
// bare member through the annotation's enum), and a switch arm whose scrutinee is
// an enum-typed parameter, self, or let (the switch lowers its arm values through
// the scrutinee's enum). The items the editor offers are exactly the ones the
// program would accept — never one it would leave undefined.
//
// The items are *added* to the value namespace rather than offered exclusively
// (unlike the member-after-dot completion, which claims its position): the value
// position genuinely admits the whole namespace too — a const of the enum type, a
// function returning it — so a bare member is one more candidate, not the only
// one. The caller prepends them so they sort first.
//
// Three superficially similar positions are deliberately omitted, because the
// lowering does not resolve a bare member there (offering one would propose a
// candidate that stays undefined — the worst outcome):
//   - a let initializer and an assignment right-hand side, whose values lower
//     under the plain binder with no expected-enum channel;
//   - a comparison right-hand side (rarity == Common), whose desugared method-call
//     argument neither bidirectional channel reaches;
//   - an associated constant inside an impl block, resolved on a path that does
//     not supply the annotation's enum.
func expectedEnumItems(doc view, root cst.Tree, offset int) []protocol.CompletionItem {
	def := expectedEnumAt(doc, root, offset)
	if def == nil {
		return nil
	}
	return enumMemberItems(def)
}

// enumMemberItems is one completion item per member of an enum definition,
// labelled with its base value (= 1) and kinded as an enum member — the bare
// form of the member-after-dot items, for an expected-enum value position.
func enumMemberItems(def *ir.TypeDef) []protocol.CompletionItem {
	if def.Enum == nil {
		return nil
	}
	kind := protocol.CompletionItemKindEnumMember
	items := make([]protocol.CompletionItem, 0, len(def.Enum.Members))
	for _, m := range def.Enum.Members {
		items = append(items, protocol.CompletionItem{
			Label:  m.Name,
			Kind:   &kind,
			Detail: "= " + m.Value.String(),
		})
	}
	return items
}

// expectedEnumAt returns the enum the value position at offset expects a bare
// member of, or nil when the position is not an enum-expecting one. It descends
// the concrete tree to the value position — probing back over the trailing
// whitespace of an empty initializer, so an explicit completion in a fresh
// "= " slot still finds its context — and reads the expected enum from the
// enclosing construct's static type the same way the lowering does.
func expectedEnumAt(doc view, root cst.Tree, offset int) *ir.TypeDef {
	if def := enumContextAt(doc, root, offset); def != nil {
		return def
	}
	// The cursor usually sits just past the partial word being typed (or at the
	// end of file), so probe one byte back, the way the type/value classifier
	// does — that lands inside a NameRef being typed (const x: Rarity = C|).
	if offset > 0 {
		if def := enumContextAt(doc, root, offset-1); def != nil {
			return def
		}
	}
	// An empty value slot is trailing whitespace just past the "=" (or a fresh
	// switch arm line); step back over it onto the last non-space byte so the
	// descent lands inside the construct, not in the gap before the next one.
	if anchor, ok := valueAnchor(doc, offset); ok {
		return enumContextAt(doc, root, anchor)
	}
	return nil
}

// valueAnchor steps offset back over the run of spaces and tabs immediately
// before it, returning the offset of the last non-space byte — the position of an
// empty initializer's "=", where the enclosing construct's span still reaches. It
// stops at a newline (a value never continues onto the previous line here) and
// reports false when offset was not on trailing space or nothing but spaces
// precedes it.
func valueAnchor(doc view, offset int) (int, bool) {
	buf := doc.Buffer()
	i := offset
	for i > 0 {
		b := buf.Slice(i-1, i)
		if len(b) == 0 || (b[0] != ' ' && b[0] != '\t') {
			break
		}
		i--
	}
	if i == offset || i == 0 {
		return 0, false
	}
	return i - 1, true
}

// enumContextAt descends the concrete tree to the node at offset, tracking the
// enclosing switch, and resolves the expected enum of the value position it
// finds — a top-level const initializer or a switch arm's value pattern. These
// are the only two positions masterbelt's lowering resolves a bare member
// through; a let initializer, an assignment, and a comparison are deliberately
// excluded (their bare members stay undefined), so the editor never offers a
// candidate the program would not accept. A position in neither (or one whose
// expected type is not an enum) yields nil.
func enumContextAt(doc view, root cst.Tree, offset int) *ir.TypeDef {
	node := root
	var enclosingSwitch cst.Tree
	haveSwitch := false
	for {
		child, found := childContaining(node, offset)
		if !found {
			break
		}
		if k, ok := node.Kind(); ok {
			switch k {
			case cst.ConstDecl:
				if inInitializerValue(child, offset) {
					return constDeclEnum(doc, node)
				}
			case cst.SwitchStmt:
				enclosingSwitch, haveSwitch = node, true
				// A fresh, still-empty arm line is whitespace directly under the
				// switch (no SwitchArm node yet): the child the cursor sits in is not
				// an arm, so the descent would never reach the SwitchArm case. Treat
				// a position in the arm region (inside the braces) as the value
				// position — but only when an arm separator (the "{", a newline, or a
				// "," ) precedes it, so the trailing gap after an unterminated arm
				// body (Common -> return |) is not mistaken for a new arm.
				if k, ok := child.Kind(); (!ok || k != cst.SwitchArm) &&
					inSwitchArmRegion(node, offset) && atArmStart(doc, offset) {
					return switchArmEnum(doc, node, offset)
				}
			case cst.SwitchArm:
				if haveSwitch && beforeArrow(node, offset) {
					return switchArmEnum(doc, enclosingSwitch, offset)
				}
			}
		}
		node = child
	}
	return nil
}

// inInitializerValue reports whether the descent into a ConstDecl is entering its
// Initializer's value — child is the Initializer node and offset sits at or past
// its "=" token. The annotation (the TypeClause) and the name are other children,
// so reaching the Initializer at the "=" marks the value position.
func inInitializerValue(child cst.Tree, offset int) bool {
	k, ok := child.Kind()
	if !ok || k != cst.Initializer {
		return false
	}
	return offset >= assignStart(child)
}

// atArmStart reports whether offset begins a fresh switch arm: stepping back over
// spaces and tabs lands on an arm separator — a newline, the "{", or a "," — and
// not on the content of a preceding statement (an arm body still being typed). It
// is what keeps the empty-arm completion off the trailing gap after an
// unterminated arm body.
func atArmStart(doc view, offset int) bool {
	buf := doc.Buffer()
	i := offset
	for i > 0 {
		b := buf.Slice(i-1, i)
		if len(b) == 0 || (b[0] != ' ' && b[0] != '\t') {
			break
		}
		i--
	}
	if i == 0 {
		return true
	}
	b := buf.Slice(i-1, i)
	return len(b) == 1 && (b[0] == '\n' || b[0] == '{' || b[0] == ',')
}

// inSwitchArmRegion reports whether offset sits inside a switch's arm region —
// after its "{" and before its "}" — the area an arm value goes, as opposed to
// the scrutinee before the brace. It is how a still-empty arm line (no SwitchArm
// node yet) is recognised as the value position.
func inSwitchArmRegion(sw cst.Tree, offset int) bool {
	open, close := -1, -1
	for _, c := range sw.Children() {
		if tok, ok := c.Token(); ok {
			switch tok.Kind() {
			case token.LBrace:
				if open < 0 {
					open = c.End()
				}
			case token.RBrace:
				close = c.Offset()
			}
		}
	}
	if open < 0 {
		return false
	}
	if close < 0 {
		close = sw.End() // an unclosed switch: the region runs to its end
	}
	return offset >= open && offset <= close
}

// beforeArrow reports whether offset sits before a switch arm's "->" — its value
// patterns, where a bare member is matched against the scrutinee (the arm body
// is after the arrow).
func beforeArrow(arm cst.Tree, offset int) bool {
	for _, c := range arm.Children() {
		if tok, ok := c.Token(); ok && tok.Kind() == token.Arrow {
			return offset < c.Offset()
		}
	}
	return true // no arrow yet (the arm is being typed): still in the patterns
}

// assignStart returns the start offset of a ConstDecl Initializer's "=" token —
// the boundary the value position begins at. An Initializer missing the "=" (a
// recovered one) reports its end, so no position counts as the value side.
func assignStart(node cst.Tree) int {
	for _, c := range node.Children() {
		if tok, ok := c.Token(); ok && tok.Kind() == token.Assign {
			return c.Offset()
		}
	}
	return node.End()
}

// constDeclEnum resolves the enum a const declaration's annotation names, by
// matching its concrete node to its AST node and resolving the written
// annotation — the same channel the const initializer's bare member folds
// through.
func constDeclEnum(doc view, node cst.Tree) *ir.TypeDef {
	decl := constDeclOf(doc, node)
	if decl == nil {
		return nil
	}
	return doc.EnumOfAnnotation(decl.Type)
}

// switchArmEnum resolves the enum a switch arm's scrutinee is typed as — the
// expected type its bare-member patterns resolve through. The scrutinee's static
// type is read syntactically from the enclosing body (a parameter's annotation,
// self's receiver type, or a let local's settled type), the same parameter / self
// / let channel the lowering's ExpectedEnum uses, so the editor offers exactly
// the members the lowering would resolve.
func switchArmEnum(doc view, sw cst.Tree, offset int) *ir.TypeDef {
	scrutinee, ok := switchScrutinee(sw)
	if !ok {
		return nil
	}
	typ, ok := scrutineeType(doc, scrutinee, offset)
	if !ok {
		return nil
	}
	return doc.EnumOf(typ)
}

// switchScrutinee returns the scrutinee node of a switch statement — the value
// expression between the switch keyword and the "{" (a NameRef or a SelfExpr in
// the channels the lowering reads).
func switchScrutinee(sw cst.Tree) (cst.Tree, bool) {
	for _, c := range sw.Children() {
		if k, ok := c.Kind(); ok {
			switch k {
			case cst.NameRef, cst.SelfExpr:
				return c, true
			}
		}
	}
	return cst.Tree{}, false
}

// scrutineeType returns the static type a switch scrutinee node names, read from
// the enclosing callable's signature and body: a parameter's resolved type,
// self's receiver type, or a let local's settled type — the parameter / self /
// let channel the lowering's ExpectedEnum reads, never the type query.
func scrutineeType(doc view, scrutinee cst.Tree, offset int) (ir.Type, bool) {
	if k, ok := scrutinee.Kind(); ok && k == cst.SelfExpr {
		if owner, ok := enclosingTypeDef(doc, offset); ok {
			return &ir.Named{Def: owner}, true
		}
		return nil, false
	}
	name, ok := identText(doc, scrutinee)
	if !ok {
		return nil, false
	}
	// A let local shadows a same-named parameter, so it is read first.
	if body, found := enclosingBody(doc, offset, doc.Trees()); found {
		if typ, bound := letTypeOf(body, name); bound {
			return typ, true
		}
	}
	if typ, ok := paramType(doc, offset, name); ok {
		return typ, true
	}
	return nil, false
}

// paramType returns the resolved type of the parameter named name in the
// callable whose body spans offset, or false when no such parameter is found.
func paramType(doc view, offset int, name string) (ir.Type, bool) {
	params, ok := enclosingParams(doc, offset)
	if !ok {
		return nil, false
	}
	for _, p := range params {
		if p.Name == name {
			return p.Type, true
		}
	}
	return nil, false
}

// enclosingParams returns the resolved parameters of the method or function
// whose declaration spans offset, or false outside one. It pairs with
// enclosingBody, navigating the same resolution-to-syntax link.
func enclosingParams(doc view, offset int) ([]ir.Param, bool) {
	trees := doc.Trees()
	m := doc.Module()
	for _, def := range m.Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			if t, ok := trees[irm.Syntax.Syntax()]; ok && within(t, offset) {
				return irm.Params, true
			}
		}
	}
	for _, fn := range m.Funcs {
		if fn.Syntax == nil {
			continue
		}
		if t, ok := trees[fn.Syntax.Syntax()]; ok && within(t, offset) {
			return fn.Params, true
		}
	}
	return nil, false
}

// enclosingTypeDef returns the type definition whose method's declaration spans
// offset — the receiver type a `switch self` reads, found through the same
// resolution-to-syntax link enclosingBody navigates.
func enclosingTypeDef(doc view, offset int) (*ir.TypeDef, bool) {
	trees := doc.Trees()
	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			if t, ok := trees[irm.Syntax.Syntax()]; ok && within(t, offset) {
				return def, true
			}
		}
	}
	return nil, false
}

// constDeclOf matches a ConstDecl concrete node to one of the file's top-level
// constants by green identity, so the resolver can read its written annotation.
// Only a top-level constant's initializer folds a bare member through its
// annotation (the assemble pass supplies the expected enum there); an associated
// constant inside an impl block resolves on a path that does not, so its node is
// deliberately left unmatched — completing a bare member there would propose a
// candidate the program leaves undefined.
func constDeclOf(doc view, node cst.Tree) *ast.ConstDecl {
	green, ok := node.Node()
	if !ok {
		return nil
	}
	for _, decl := range doc.AST().File().Decls {
		if decl.Syntax() == green {
			return decl
		}
	}
	return nil
}

// identText returns the text of the first Ident token under a node.
func identText(doc view, node cst.Tree) (string, bool) {
	buf := doc.Buffer()
	for _, c := range node.Children() {
		if tok, ok := c.Token(); ok && tok.Kind() == token.Ident {
			return c.Text(buf), true
		}
	}
	return "", false
}
