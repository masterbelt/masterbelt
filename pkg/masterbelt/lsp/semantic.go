package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
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
)

// Semantic token modifier bits, matching the legend's TokenModifiers order.
const (
	smDeclaration = 1 << iota
	smReadonly
)

// semanticLegend is advertised at initialize and tells the editor how to read
// the token type and modifier indices the server emits.
var semanticLegend = protocol.SemanticTokensLegend{
	TokenTypes:     []string{"keyword", "comment", "type", "variable", "number", "string", "operator"},
	TokenModifiers: []string{"declaration", "readonly"},
}

// rawSemanticToken is one classified token before the LSP's delta encoding.
type rawSemanticToken struct {
	line, char, length int
	tokenType, mods    int
}

// semanticTokens classifies every leaf of the concrete tree and returns the
// tokens in the LSP's relative-encoded form.
func semanticTokens(doc *abstract.Document) *protocol.SemanticTokens {
	buf := doc.Buffer()

	var raws []rawSemanticToken
	var walk func(t cst.Tree, parent cst.Kind)
	walk = func(t cst.Tree, parent cst.Kind) {
		if leaf, ok := t.Token(); ok {
			tokenType, mods, ok := classifyToken(leaf.Kind(), parent)
			if !ok {
				return
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
		for _, child := range t.Children() {
			walk(child, node.Kind())
		}
	}
	walk(doc.Concrete().Tree(), cst.File)

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

// classifyToken maps a leaf token to a semantic type and modifiers from its kind
// and the kind of the node that contains it. ok is false for elements that carry
// no colour (whitespace, newlines, EOF, illegal bytes).
func classifyToken(kind token.Kind, parent cst.Kind) (tokenType, mods int, ok bool) {
	switch kind {
	case token.Const, token.Pub:
		return stKeyword, 0, true
	case token.LineComment, token.BlockComment, token.DocComment:
		return stComment, 0, true
	case token.Int:
		return stNumber, 0, true
	case token.String:
		return stString, 0, true
	case token.Colon, token.Assign:
		return stOperator, 0, true
	case token.Ident:
		switch parent {
		case cst.TypeName:
			// A type name: a const annotation (now a full type expression) or a
			// name inside a type expression.
			return stType, 0, true
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
