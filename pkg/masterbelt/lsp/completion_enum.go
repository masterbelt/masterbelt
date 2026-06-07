package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// expectedEnumItems offers a bare enum member (Common rather than Rarity.Common)
// at the value positions masterbelt's lowering resolves one through: a top-level
// const initializer annotated with an enum, a switch arm whose scrutinee is an
// enum-typed parameter/self/let, a let initializer annotated with an enum, an
// assignment to an enum-typed let, and a comparison whose receiver is an
// enum-typed value (rarity == Common, the desugared operator argument). The items
// the editor offers are exactly the ones the program would accept — never one it
// would leave undefined.
//
// The items are *added* to the value namespace rather than offered exclusively
// (unlike the member-after-dot completion, which claims its position): the value
// position genuinely admits the whole namespace too — a const of the enum type, a
// function returning it — so a bare member is one more candidate, not the only
// one. The caller prepends them so they sort first.
//
// One superficially similar position is deliberately omitted, because the
// lowering does not resolve a bare member there (offering one would propose a
// candidate that stays undefined — the worst outcome): an associated constant
// inside an impl block referencing *another* type's enum, whose eager fold runs
// before that enum's members are settled (the qualified form does not fold there
// either — a pre-existing, separate limitation).
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
// finds — a top-level const initializer, a switch arm's value pattern, a let
// initializer, an assignment's right-hand side, or a comparison's argument. These
// are the positions masterbelt's lowering resolves a bare member through; a
// position in none (or one whose expected type is not an enum) yields nil, so the
// editor never offers a candidate the program would not accept.
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
			case cst.LetStmt:
				// The let's initializer folds a bare member through the let's
				// annotation enum — the body twin of the const initializer.
				if inInitializerValue(child, offset) {
					return letStmtEnum(doc, node, offset)
				}
			case cst.AssignStmt:
				// An assignment's right-hand side folds a bare member through the
				// target local's enum. The value follows the "=" token; the target
				// (before it) is an ordinary value position.
				if offset >= assignStart(node) {
					return assignStmtEnum(doc, node, offset)
				}
			case cst.BinaryExpr:
				// A comparison's argument (rarity == Common) folds a bare member
				// through the receiver's enum — the desugared operator argument. The
				// right operand follows the operator; the left is the receiver.
				if def := binaryExprEnum(doc, node, offset); def != nil {
					return def
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
	openAt, closeAt := -1, -1
	for _, c := range sw.Children() {
		if tok, ok := c.Token(); ok {
			switch tok.Kind() {
			case token.LBrace:
				if openAt < 0 {
					openAt = c.End()
				}
			case token.RBrace:
				closeAt = c.Offset()
			}
		}
	}
	if openAt < 0 {
		return false
	}
	if closeAt < 0 {
		closeAt = sw.End() // an unclosed switch: the region runs to its end
	}
	return offset >= openAt && offset <= closeAt
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

// letStmtEnum resolves the enum a let statement's annotation names — the channel
// its initializer's bare member folds through, the body twin of constDeclEnum. It
// reads the binding's settled type from the enclosing body (the same letTypeOf
// reading the switch-local path uses), so the members offered are exactly the ones
// the let initializer's lowering would resolve.
func letStmtEnum(doc view, node cst.Tree, offset int) *ir.TypeDef {
	name, ok := letBindingName(doc, node)
	if !ok {
		return nil
	}
	body, found := enclosingBody(doc, offset, doc.Trees())
	if !found {
		return nil
	}
	typ, bound := letTypeOf(body, name)
	if !bound {
		return nil
	}
	return doc.EnumOf(typ)
}

// assignStmtEnum resolves the enum the target local of an assignment is typed as
// — the channel its right-hand side's bare member folds through. The target is the
// assignment's first child (a NameRef naming the let local); its static type is
// read the same parameter / self / let way a scrutinee's is.
func assignStmtEnum(doc view, node cst.Tree, offset int) *ir.TypeDef {
	target, ok := firstValueChild(node)
	if !ok {
		return nil
	}
	typ, ok := scrutineeType(doc, target, offset)
	if !ok {
		return nil
	}
	return doc.EnumOf(typ)
}

// binaryExprEnum resolves the enum a comparison's receiver is typed as, when the
// cursor sits in the right operand of a comparison operator — the desugared
// operator argument (rarity == Common becomes rarity.eql(Common)). It returns nil
// for a non-comparison operator, a cursor in the left operand, or a receiver whose
// static type names no enum, so a bare member is offered only where the lowering
// resolves one.
func binaryExprEnum(doc view, node cst.Tree, offset int) *ir.TypeDef {
	recv, opStart, ok := comparisonReceiver(node)
	if !ok || offset < opStart {
		return nil // not a comparison, or the cursor is in the left operand
	}
	typ, ok := scrutineeType(doc, recv, offset)
	if !ok {
		return nil
	}
	return doc.EnumOf(typ)
}

// letBindingName returns the name a let statement binds — its first Ident child,
// after the "let" keyword.
func letBindingName(doc view, node cst.Tree) (string, bool) {
	buf := doc.Buffer()
	for _, c := range node.Children() {
		if tok, ok := c.Token(); ok && tok.Kind() == token.Ident {
			return c.Text(buf), true
		}
	}
	return "", false
}

// firstValueChild returns the first value-expression child of a node — the
// assignment target (a NameRef or SelfExpr) before the "=".
func firstValueChild(node cst.Tree) (cst.Tree, bool) {
	for _, c := range node.Children() {
		if k, ok := c.Kind(); ok {
			switch k {
			case cst.NameRef, cst.SelfExpr:
				return c, true
			}
		}
		// Stop at the "=" token: the target precedes it.
		if tok, ok := c.Token(); ok && tok.Kind() == token.Assign {
			return cst.Tree{}, false
		}
	}
	return cst.Tree{}, false
}

// comparisonReceiver returns a binary expression's left operand (the receiver an
// operator desugars its call onto), the start offset of the comparison operator
// token, and whether the operator is a comparison — the operators whose desugared
// method takes the receiver's enum (==, !=, <, <=, >, >=). A non-comparison
// operator (+, &&) reports false: its argument is not an enum member position. The
// operator's start is the boundary the right operand begins past, so the position
// just after an "=="  — where an empty value slot's anchor lands — counts as the
// argument position.
func comparisonReceiver(node cst.Tree) (recv cst.Tree, opStart int, ok bool) {
	var left cst.Tree
	haveLeft := false
	for _, c := range node.Children() {
		if !haveLeft {
			if k, kok := c.Kind(); kok {
				switch k {
				case cst.NameRef, cst.SelfExpr:
					left, haveLeft = c, true
				}
			}
			continue
		}
		if tok, tok2 := c.Token(); tok2 {
			switch tok.Kind() {
			case token.EqEq, token.BangEq, token.Lt, token.LtEq, token.Gt, token.GtEq:
				return left, c.Offset(), true
			}
		}
	}
	return cst.Tree{}, 0, false
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
