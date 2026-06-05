package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
	protocol "github.com/owenrumney/go-lsp/lsp"
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
	return semanticTokensWith(doc, nil, nil, nil)
}

// semanticTokensIn classifies with the program's resolution layered over the
// lexical pass: a member access that names an imported constant (geo.Origin)
// renders as the constant it is, not as a property, a call's callee that
// names a type renders as the type the conversion constructs (error("msg")),
// and one that names a top-level function renders as the function it calls,
// not as a value reference.
func semanticTokensIn(v view) *protocol.SemanticTokens {
	typeNames := map[string]bool{}
	for _, t := range v.TypeNames() {
		typeNames[t.Name] = true
	}
	members := map[*cst.Node]*ast.MemberExpr{}
	funcCallees := map[*cst.Node]bool{}
	typeCallees := map[*cst.Node]bool{}
	forEachExpr(v.AST().File(), func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.MemberExpr:
			members[e.Syntax()] = e
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
		m, ok := members[green]
		return ok && v.ResolveMember(m) != nil
	}, func(green *cst.Node) bool {
		return funcCallees[green]
	}, func(green *cst.Node) bool {
		return typeCallees[green]
	})
}

// semanticTokensWith is the classification walk. isImportedConst,
// isFuncCallee, and isTypeCallee, when set, are the classifications a lexical
// pass cannot make: whether a member-access node resolves to an imported
// constant, and whether a name-reference node is a call's callee resolving to
// a function or to a type (a conversion).
func semanticTokensWith(doc *abstract.Document, isImportedConst, isFuncCallee, isTypeCallee func(*cst.Node) bool) *protocol.SemanticTokens {
	buf := doc.Buffer()

	var raws []rawSemanticToken
	// walk carries the leaf-classification context of t's parent: its kind,
	// its green node, and whether the parent is the callee member access of a
	// call (which makes the member name a method). selfCallee says t itself is
	// one, propagated to t's own children.
	var walk func(t cst.Tree, parent cst.Kind, parentGreen *cst.Node, parentIsCallee, selfIsCallee bool)
	walk = func(t cst.Tree, parent cst.Kind, parentGreen *cst.Node, parentIsCallee, selfIsCallee bool) {
		if leaf, ok := t.Token(); ok {
			tokenType, mods, ok := classifyToken(leaf.Kind(), parent, parentIsCallee)
			if !ok {
				return
			}
			if leaf.Kind() == token.Ident && parent == cst.MemberExpr && isImportedConst != nil && isImportedConst(parentGreen) {
				tokenType, mods = stVariable, smReadonly
			}
			if leaf.Kind() == token.Ident && parent == cst.NameRef && isFuncCallee != nil && isFuncCallee(parentGreen) {
				tokenType, mods = stFunction, 0
			}
			if leaf.Kind() == token.Ident && parent == cst.NameRef && isTypeCallee != nil && isTypeCallee(parentGreen) {
				tokenType, mods = stType, 0
			}
			startLine, startChar := buf.LineColumn(t.Offset(), source.UTF16Encoding)
			endLine, endChar := buf.LineColumn(t.End(), source.UTF16Encoding)
			if startLine != endLine {
				// LSP semantic tokens are single-line; a multi-line block comment
				// is left to the grammar.
				return
			}
			raws = append(raws, rawSemanticToken{startLine, startChar, endChar - startChar, tokenType, mods})
			return
		}
		node, _ := t.Node()
		// The callee of a call is its first child node; when it is a member
		// access, the member's name is the method being called.
		var callee cst.Green
		if node.Kind() == cst.CallExpr {
			for _, child := range t.Children() {
				if n, isNode := child.Node(); isNode {
					if n.Kind() == cst.MemberExpr {
						callee = child.Green()
					}
					break
				}
			}
		}
		for _, child := range t.Children() {
			walk(child, node.Kind(), node, selfIsCallee, callee != nil && child.Green() == callee)
		}
	}
	walk(doc.Concrete().Tree(), cst.File, nil, false, false)

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
	return &protocol.SemanticTokens{Data: data}
}

// classifyToken maps a leaf token to a semantic type and modifiers from its
// kind, the kind of the node that contains it, and — for a member access —
// whether that access is the callee of a call. ok is false for elements that
// carry no colour (whitespace, newlines, EOF, illegal bytes).
func classifyToken(kind token.Kind, parent cst.Kind, calleeMember bool) (tokenType, mods int, ok bool) {
	switch kind {
	case token.Const, token.Pub, token.Assert, token.Where, token.Type, token.Enum, token.Impl,
		token.Fn, token.Return, token.Self, token.Null, token.Extern, token.Builtin,
		token.Use, token.From, token.True, token.False,
		token.Io, token.Async, token.Nondet, token.Await:
		// Every keyword, uniformly — the cold-start grammar colours the same
		// set keyword.control, so the two layers cannot drift apart per word.
		return stKeyword, 0, true
	case token.LineComment, token.BlockComment, token.DocComment:
		return stComment, 0, true
	case token.Int, token.DatetimeLit, token.DurationLit:
		// The datetime and duration literals colour as numbers: their
		// cold-start grammar scopes are constant.numeric too.
		return stNumber, 0, true
	case token.String:
		return stString, 0, true
	case token.Colon, token.Assign, token.Arrow:
		return stOperator, 0, true
	case token.Ident:
		switch parent {
		case cst.TypeName:
			// A type name: a const annotation (now a full type expression) or a
			// name inside a type expression.
			return stType, 0, true
		case cst.TypeDecl:
			// The declared type's own name.
			return stType, smDeclaration, true
		case cst.EnumDecl:
			// The declared enum's own name — a nominal type.
			return stType, smDeclaration, true
		case cst.EnumMember:
			// A member's declared name (Common, Rare) reads as an enum member, a
			// read-only named value.
			return stEnumMember, smDeclaration | smReadonly, true
		case cst.GenericParam:
			// A declared type parameter — its uses in the body sit in
			// TypeName nodes and classify as types, so the declaration
			// matches them.
			return stType, smDeclaration, true
		case cst.UseDecl:
			// The namespace a use declaration binds (use geo from ...).
			return stNamespace, smDeclaration, true
		case cst.Field:
			// A record field's declared name ({ id: int8 }).
			return stProperty, smDeclaration, true
		case cst.RecordField:
			// A field initializer's name in a record literal (Point{ x: 1 })
			// reads as the field it fills.
			return stProperty, 0, true
		case cst.RecordLit:
			// The typed form's leading type name (Point{ ... }).
			return stType, 0, true
		case cst.MethodDecl:
			// A method's declared name inside an impl block.
			return stMethod, smDeclaration, true
		case cst.FuncDecl:
			// A top-level function's declared name.
			return stFunction, smDeclaration, true
		case cst.Param:
			// A declared parameter — a method's, a function type's, or a
			// function literal's.
			return stParameter, smDeclaration, true
		case cst.MemberExpr:
			// A member access: the callee of a call names the method being
			// called (self.bump(x)); anything else reads as a property
			// (self.id) — unless resolution says it is an imported constant,
			// which the walk's override handles.
			if calleeMember {
				return stMethod, 0, true
			}
			return stProperty, 0, true
		case cst.NameRef:
			return stVariable, smReadonly, true
		case cst.ConstDecl:
			// The declaration's own name.
			return stVariable, smDeclaration | smReadonly, true
		default:
			return stVariable, 0, true
		}
	default:
		return 0, 0, false
	}
}
