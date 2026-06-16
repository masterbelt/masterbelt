package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Semantic tokens drive syntax highlighting from the real parse rather than a
// separate editor grammar: the concrete tree already knows that an identifier is
// a declared name, a type, or a value reference, so the colours come from the
// one source of truth. The editor's TextMate grammar (generated from the same
// keyword table) only covers the brief moment before the server responds.

// Semantic token type indices. They must match the order of the legend's
// TokenTypes, which is what the editor maps back to theme colours.
const (
	stKeyword = iota
	stComment
	stType
	stVariable
	stNumber
	stString
	stOperator
	stNamespace
	stProperty
	stMethod
	stParameter
	stFunction
	stEnumMember
)

// Semantic token modifier bits, matching the legend's TokenModifiers order.
const (
	smDeclaration = 1 << iota
	smReadonly
)

// semanticLegend is advertised at initialize and tells the editor how to read
// the token type and modifier indices the server emits.
var semanticLegend = protocol.SemanticTokensLegend{
	TokenTypes: []string{
		"keyword", "comment", "type", "variable", "number", "string", "operator",
		"namespace", "property", "method", "parameter", "function", "enumMember",
	},
	TokenModifiers: []string{"declaration", "readonly"},
}

// rawSemanticToken is one classified token before the LSP's delta encoding.
type rawSemanticToken struct {
	line, char, length int
	tokenType, mods    int
}

// semanticTokens classifies every leaf of the concrete tree and returns the
// tokens in the LSP's relative-encoded form, lexically — the server passes the
// program's resolution through semanticTokensIn.
func semanticTokens(doc *abstract.Document) *protocol.SemanticTokens {
	return semanticTokensWith(doc, nil, nil, nil, nil)
}

// semanticTokensIn classifies with the program's resolution layered over the
// lexical pass: a member access that names an imported constant (geo.Origin)
// or an associated constant (int8.Max) renders as the read-only value it is,
// not as a property; one that names an enum member (Rarity.Common) renders as
// an enum member, matching the declaration site; a call's callee that names a
// type renders as the type the conversion constructs (error("msg")); and one
// that names a top-level function renders as the function it calls, not as a
// value reference.
func semanticTokensIn(v view) *protocol.SemanticTokens {
	typeNames := map[string]bool{}
	for _, t := range v.TypeNames() {
		typeNames[t.Name] = true
	}
	members := map[*cst.Node]*ast.MemberExpr{}
	funcCallees := map[*cst.Node]bool{}
	typeCallees := map[*cst.Node]bool{}
	enumMembers := map[*cst.Node]bool{}
	assocConsts := map[*cst.Node]bool{}
	forEachExpr(v.AST().File(), func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.MemberExpr:
			members[e.Syntax()] = e
			switch memberValueKind(v, e) {
			case memberEnumMember:
				enumMembers[e.Syntax()] = true
			case memberAssocConst:
				assocConsts[e.Syntax()] = true
			default:
				// memberNone: the access is neither an enum member nor an
				// associated constant, so the lexical property classification
				// stands and nothing is recorded for this node.
			}
		case *ast.CallExpr:
			// A type name wins over a same-named function, as in the type rules.
			if id, ok := e.Callee.(*ast.Identifier); ok {
				switch {
				case typeNames[id.Name]:
					typeCallees[id.Syntax()] = true
				case v.ResolveFunc(id) != nil:
					funcCallees[id.Syntax()] = true
				}
			}
		}
	})
	return semanticTokensWith(v.AST(), func(green *cst.Node) bool {
		if assocConsts[green] {
			return true
		}
		m, ok := members[green]
		return ok && v.ResolveMember(m) != nil
	}, func(green *cst.Node) bool {
		return funcCallees[green]
	}, func(green *cst.Node) bool {
		return typeCallees[green]
	}, func(green *cst.Node) bool {
		return enumMembers[green]
	})
}

// memberValueKind classifies a member access whose receiver names a type:
// Rarity.Common is an enum member, int8.Max an associated constant. A receiver
// that is shadowed by a value binding, or names no type, or whose member is
// neither, is none — the lexical property classification stands.
type memberKind int

const (
	memberNone memberKind = iota
	memberEnumMember
	memberAssocConst
)

func memberValueKind(v view, m *ast.MemberExpr) memberKind {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok || v.Resolve(recv) != nil {
		return memberNone // a value shadowing the type name is a value access
	}
	def := lookupTypeName(v, recv.Name)
	if def == nil {
		return memberNone
	}
	if def.Enum != nil {
		for _, em := range def.Enum.Members {
			if em.Name == m.Member.Name {
				return memberEnumMember
			}
		}
	}
	for _, ac := range def.Consts {
		if ac.Name == m.Member.Name {
			return memberAssocConst
		}
	}
	return memberNone
}

// semanticTokensWith is the classification walk. isReadonlyConst,
// isFuncCallee, isTypeCallee, and isEnumMember, when set, are the
// classifications a lexical pass cannot make: whether a member-access node
// resolves to a read-only constant (an imported or associated constant) or to
// an enum member, and whether a name-reference node is a call's callee
// resolving to a function or to a type (a conversion).
func semanticTokensWith(doc *abstract.Document, isReadonlyConst, isFuncCallee, isTypeCallee, isEnumMember func(*cst.Node) bool) *protocol.SemanticTokens {
	res := semanticResolution{
		isReadonlyConst: isReadonlyConst,
		isFuncCallee:    isFuncCallee,
		isTypeCallee:    isTypeCallee,
		isEnumMember:    isEnumMember,
	}
	buf := doc.Buffer()

	var raws []rawSemanticToken
	// walk carries the leaf-classification context of t's parent: its kind,
	// its green node, and whether the parent is the callee member access of a
	// call (which makes the member name a method). selfCallee says t itself is
	// one, propagated to t's own children.
	var walk func(t cst.Tree, parent cst.Kind, parentGreen *cst.Node, parentIsCallee, selfIsCallee, typeNameSeg bool)
	walk = func(t cst.Tree, parent cst.Kind, parentGreen *cst.Node, parentIsCallee, selfIsCallee, typeNameSeg bool) {
		if leaf, ok := t.Token(); ok {
			if raw, ok := classifySemanticLeaf(buf, t, leaf, parent, parentGreen, parentIsCallee, typeNameSeg, res); ok {
				raws = append(raws, raw)
			}
			return
		}
		node, _ := t.Node()
		callee := calleeMemberGreen(node, t)
		// In a dotted type name the head is the first name token; every name
		// token after it is a projection segment (Order.customer.id), which a
		// keyword segment colours by its role rather than as a keyword. A method
		// declaration's name is the first name token that is not a marker
		// (pub/extern/fn/effect) — `fn where(...)` — coloured the same way.
		headSeen, methodNameSeen := false, false
		for _, child := range t.Children() {
			seg := false
			if tok, isTok := child.Token(); isTok && isNameToken(tok.Kind()) {
				switch node.Kind() {
				case cst.TypeName:
					seg = headSeen
					headSeen = true
				case cst.MethodDecl:
					if !methodNameSeen && !tok.Kind().MethodMarker() {
						seg, methodNameSeen = true, true
					}
				default:
					// Other nodes carry no name-token disambiguation here.
				}
			}
			walk(child, node.Kind(), node, selfIsCallee, callee != nil && child.Green() == callee, seg)
		}
	}
	walk(doc.Concrete().Tree(), cst.File, nil, false, false, false)

	return &protocol.SemanticTokens{Data: encodeSemanticTokens(raws)}
}

// semanticResolution bundles the four resolution-aware classifiers a lexical
// pass cannot make, each nil for the purely lexical pass.
type semanticResolution struct {
	isReadonlyConst func(*cst.Node) bool
	isFuncCallee    func(*cst.Node) bool
	isTypeCallee    func(*cst.Node) bool
	isEnumMember    func(*cst.Node) bool
}

// classifySemanticLeaf classifies a single leaf token into a raw semantic
// token, layering the resolution-aware overrides over the lexical
// classification and dropping anything uncoloured or spanning multiple lines.
func classifySemanticLeaf(buf source.Buffer, t cst.Tree, leaf *cst.Token, parent cst.Kind, parentGreen *cst.Node, parentIsCallee, typeNameSeg bool, res semanticResolution) (rawSemanticToken, bool) {
	tokenType, mods, ok := classifyToken(leaf.Kind(), parent, parentIsCallee, typeNameSeg)
	if !ok {
		return rawSemanticToken{}, false
	}
	tokenType, mods = res.override(leaf.Kind(), parent, parentGreen, tokenType, mods)
	startLine, startChar := buf.LineColumn(t.Offset(), source.UTF16Encoding)
	endLine, endChar := buf.LineColumn(t.End(), source.UTF16Encoding)
	if startLine != endLine {
		// LSP semantic tokens are single-line; a multi-line block comment
		// is left to the grammar.
		return rawSemanticToken{}, false
	}
	return rawSemanticToken{startLine, startChar, endChar - startChar, tokenType, mods}, true
}

// override applies the resolution-aware reclassification to an identifier leaf:
// a member access naming a read-only constant or an enum member, or a name
// reference that is a call's callee naming a function or a type. A non-ident
// leaf or an absent resolver leaves the lexical classification untouched. The
// checks run in the original walk's order, so a later match wins over an
// earlier one (an enum member over a read-only constant, a type over a
// function callee) — preserving the prior sequential-override behaviour.
func (res semanticResolution) override(kind token.Kind, parent cst.Kind, parentGreen *cst.Node, tokenType, mods int) (int, int) {
	if kind != token.Ident {
		return tokenType, mods
	}
	switch parent {
	case cst.MemberExpr:
		if res.isReadonlyConst != nil && res.isReadonlyConst(parentGreen) {
			tokenType, mods = stVariable, smReadonly
		}
		if res.isEnumMember != nil && res.isEnumMember(parentGreen) {
			tokenType, mods = stEnumMember, smReadonly
		}
	case cst.NameRef:
		if res.isFuncCallee != nil && res.isFuncCallee(parentGreen) {
			tokenType, mods = stFunction, 0
		}
		if res.isTypeCallee != nil && res.isTypeCallee(parentGreen) {
			tokenType, mods = stType, 0
		}
	default:
		// Any other parent leaves the leaf's lexical classification standing.
	}
	return tokenType, mods
}

// calleeMemberGreen returns the green node of a call's callee when that callee
// is a member access (so the member name colours as the method called), or nil
// for any other node. The callee is the call's first child node.
func calleeMemberGreen(node *cst.Node, t cst.Tree) cst.Green {
	if node.Kind() != cst.CallExpr {
		return nil
	}
	for _, child := range t.Children() {
		if n, isNode := child.Node(); isNode {
			if n.Kind() == cst.MemberExpr {
				return child.Green()
			}
			break
		}
	}
	return nil
}

// encodeSemanticTokens turns the classified tokens into the LSP's relative
// (delta line, delta char, length, type, modifiers) encoding.
func encodeSemanticTokens(raws []rawSemanticToken) []int {
	data := make([]int, 0, len(raws)*5)
	prevLine, prevChar := 0, 0
	for _, r := range raws {
		deltaLine := r.line - prevLine
		deltaChar := r.char
		if deltaLine == 0 {
			deltaChar = r.char - prevChar
		}
		data = append(data, deltaLine, deltaChar, r.length, r.tokenType, r.mods)
		prevLine, prevChar = r.line, r.char
	}
	return data
}

// classifyToken maps a leaf token to a semantic type and modifiers from its
// kind, the kind of the node that contains it, and — for a member access —
// whether that access is the callee of a call. ok is false for elements that
// carry no colour (whitespace, newlines, EOF, illegal bytes).
func classifyToken(kind token.Kind, parent cst.Kind, calleeMember, typeNameSeg bool) (tokenType, mods int, ok bool) {
	switch kind {
	case token.Const, token.Pub, token.Assert, token.Where, token.Type, token.Enum, token.Interface, token.Impl,
		token.Fn, token.Return, token.Self, token.Null, token.Extern, token.Builtin,
		token.Use, token.From, token.True, token.False,
		token.Io, token.Async, token.Nondet, token.Await, token.Switch, token.Match, token.If, token.Else, token.Let,
		token.For, token.Of, token.In:
		// A reserved word read as a name (the parser's nameLike: a member, a
		// record field, a record-literal field, a parameter, or a type-position
		// projection segment) colours as that name's role, not as the keyword — the
		// same classification its identifier sibling would take. A bare type keyword
		// (null/self/type as a TypeName head, not a projection segment) and every
		// keyword elsewhere stay keyword.control, matching the cold-start grammar.
		if isNamePosition(parent) || ((parent == cst.TypeName || parent == cst.MethodDecl) && typeNameSeg) {
			return classifyIdent(parent, calleeMember)
		}
		return stKeyword, 0, true
	case token.LineComment, token.BlockComment, token.DocComment:
		return stComment, 0, true
	case token.Int, token.BinInt, token.OctInt, token.HexInt, token.DatetimeLit, token.DurationLit:
		// The radix, datetime, and duration literals colour as numbers: their
		// cold-start grammar scopes are constant.numeric too.
		return stNumber, 0, true
	case token.String:
		return stString, 0, true
	case token.Colon, token.Assign, token.Arrow, token.Question, token.DotDot, token.DotDotDot:
		// The ternary "?" colours as an operator; its ":" is the same Colon token
		// a type clause uses, already covered above. The range operators ".." and
		// "..." colour as operators too — the surface syntax of the range builtin.
		return stOperator, 0, true
	case token.Ident:
		return classifyIdent(parent, calleeMember)
	default:
		return 0, 0, false
	}
}

// identClass is the colour an identifier leaf takes from its parent node kind.
type identClass struct {
	tokenType int
	mods      int
}

// identClasses maps a parent node kind to the fixed colour its identifier child
// takes. The comments on each entry record why that kind colours the way it
// does; a parent kind not listed here is an ordinary value identifier, and
// cst.MemberExpr is handled in classifyIdent because its colour depends on
// whether the access is a call's callee.
var identClasses = map[cst.Kind]identClass{
	// A type name: a const annotation (now a full type expression) or a name
	// inside a type expression.
	cst.TypeName: {stType, 0},
	// The declared type's own name.
	cst.TypeDecl: {stType, smDeclaration},
	// The declared enum's own name — a nominal type.
	cst.EnumDecl: {stType, smDeclaration},
	// The declared interface's own name — a nominal behaviour, coloured as a
	// type (it is written in type-requirement positions).
	cst.InterfaceDecl: {stType, smDeclaration},
	// An interface member's declared name (a required or provided method).
	cst.InterfaceMember: {stMethod, smDeclaration},
	// A member's declared name (Common, Rare) reads as an enum member, a
	// read-only named value.
	cst.EnumMember: {stEnumMember, smDeclaration | smReadonly},
	// A declared type parameter — its uses in the body sit in TypeName nodes
	// and classify as types, so the declaration matches them.
	cst.GenericParam: {stType, smDeclaration},
	// The namespace a use declaration binds (use geo from ...).
	cst.UseDecl: {stNamespace, smDeclaration},
	// A record field's declared name ({ id: int8 }).
	cst.Field: {stProperty, smDeclaration},
	// A field initializer's name in a record literal (Point{ x: 1 }) reads as
	// the field it fills.
	cst.RecordField: {stProperty, 0},
	// The typed form's leading type name (Point{ ... }).
	cst.RecordLit: {stType, 0},
	// An accessor/static modifier (get/set/static) is a context keyword: the
	// lexer leaves it an identifier, but inside a Modifier node it colours as a
	// keyword, the same set keyword.control covers.
	cst.Modifier: {stKeyword, 0},
	// A master/record/primary context keyword: the lexer leaves it an identifier,
	// but inside a MasterKeyword node it colours as a keyword, exactly as the
	// accessor modifiers above do.
	cst.MasterKeyword: {stKeyword, 0},
	// The declared master's own name — a nominal data table, coloured as a type.
	cst.MasterDecl: {stType, smDeclaration},
	// A primary-key column name (the key columns are the direct Ident children of
	// the primary member) reads as a property — it names a record field.
	cst.MasterPrimary: {stProperty, 0},
	// A method's declared name inside an impl block.
	cst.MethodDecl: {stMethod, smDeclaration},
	// A top-level function's declared name.
	cst.FuncDecl: {stFunction, smDeclaration},
	// A declared parameter — a method's, a function type's, or a function
	// literal's.
	cst.Param:   {stParameter, smDeclaration},
	cst.NameRef: {stVariable, smReadonly},
	// A let's bound name — a mutable block-local, so it carries the declaration
	// modifier but not readonly (unlike a const).
	cst.LetStmt: {stVariable, smDeclaration},
	// The declaration's own name.
	cst.ConstDecl: {stVariable, smDeclaration | smReadonly},
}

// isNamePosition reports whether a node kind contains a reserved word used as a
// name directly, by parent kind alone: a member access (the member is always the
// keyword), a record field or record-literal field name, or a parameter name. A
// type-position projection is the one ambiguous case — a TypeName holds both a
// bare type keyword (null/self/type, the head) and a projection segment — so it
// is decided by position, not here (see the typeNameSeg flag in classifyToken).
func isNamePosition(parent cst.Kind) bool {
	switch parent {
	case cst.MemberExpr, cst.Field, cst.RecordField, cst.Param:
		return true
	default:
		return false
	}
}

// isNameToken reports whether a token can be a name segment of a dotted type
// name — an identifier or a reserved word read as one — so the walk can tell a
// TypeName's head from its projection segments.
func isNameToken(kind token.Kind) bool {
	return kind == token.Ident || kind.Keyword()
}

// classifyIdent colours an identifier leaf from the kind of node that contains
// it — a declared name, a type position, a member or value reference — and,
// for a member access, whether that access is the callee of a call (which makes
// the member name a method). A parent kind not in the colour table is an
// ordinary value identifier.
func classifyIdent(parent cst.Kind, calleeMember bool) (tokenType, mods int, ok bool) {
	if parent == cst.MemberExpr {
		// A member access: the callee of a call names the method being
		// called (self.bump(x)); anything else reads as a property
		// (self.id) — unless resolution says it is an imported constant,
		// which the walk's override handles.
		if calleeMember {
			return stMethod, 0, true
		}
		return stProperty, 0, true
	}
	if c, found := identClasses[parent]; found {
		return c.tokenType, c.mods, true
	}
	return stVariable, 0, true
}
