package lsp

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
)

type decodedToken struct {
	line, char, length, tokenType, mods int
}

// decode reverses the LSP relative encoding back into absolute tokens.
func decode(data []int) []decodedToken {
	var out []decodedToken
	line, char := 0, 0
	for i := 0; i+5 <= len(data); i += 5 {
		deltaLine, deltaChar := data[i], data[i+1]
		if deltaLine == 0 {
			char += deltaChar
		} else {
			line += deltaLine
			char = deltaChar
		}
		out = append(out, decodedToken{line, char, data[i+2], data[i+3], data[i+4]})
	}
	return out
}

func TestSemanticTokens(t *testing.T) {
	doc := abstract.NewDocument([]byte("pub const X: int64 = 100\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 3, stKeyword, 0},                            // pub
		{0, 4, 5, stKeyword, 0},                            // const
		{0, 10, 1, stVariable, smDeclaration | smReadonly}, // X (declared name)
		{0, 11, 1, stOperator, 0},                          // :
		{0, 13, 5, stType, 0},                              // int64
		{0, 19, 1, stOperator, 0},                          // =
		{0, 21, 3, stNumber, 0},                            // 100
	}

	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d:\n got:  %+v\n want: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSemanticTokensStringLiteral(t *testing.T) {
	doc := abstract.NewDocument([]byte("const X = \"label\"\n"))
	got := decode(semanticTokens(doc).Data)
	// The initializer "label" (8 columns: the quotes included) is a string token.
	last := got[len(got)-1]
	want := decodedToken{0, 10, 7, stString, 0}
	if last != want {
		t.Errorf("string literal token = %+v, want %+v", last, want)
	}
}

func TestSemanticTokensNameRefIsReadonlyVariable(t *testing.T) {
	doc := abstract.NewDocument([]byte("const Alias = MaxLevel\n"))
	got := decode(semanticTokens(doc).Data)
	// Last token is the value reference "MaxLevel": a read-only variable, not a
	// declaration.
	last := got[len(got)-1]
	if last.tokenType != stVariable || last.mods != smReadonly {
		t.Errorf("value reference token = %+v, want variable/readonly", last)
	}
}

func TestSemanticTokensSkipsMultilineBlockComment(t *testing.T) {
	doc := abstract.NewDocument([]byte("/* a\n b */ const X = 1\n"))
	got := decode(semanticTokens(doc).Data)

	for _, tok := range got {
		if tok.tokenType == stComment {
			t.Fatalf("multi-line block comment should be skipped, got comment token %+v", tok)
		}
	}
	// The first emitted token is `const`, on the comment's closing line.
	if len(got) == 0 || got[0].tokenType != stKeyword || got[0].line != 1 {
		t.Fatalf("first token = %+v, want a keyword on line 1", got)
	}
}

func TestSemanticTokensQualifiedTypeName(t *testing.T) {
	// Both halves of a qualified type annotation (geo.Point) classify as type
	// tokens: the qualifier has no colour of its own in the legend, and the
	// whole dotted form names one type.
	doc := abstract.NewDocument([]byte("const start: geo.Point = 1\n"))
	got := decode(semanticTokens(doc).Data)

	var typed []decodedToken
	for _, tok := range got {
		if tok.tokenType == stType {
			typed = append(typed, tok)
		}
	}
	want := []decodedToken{
		{0, 13, 3, stType, 0}, // geo
		{0, 17, 5, stType, 0}, // Point
	}
	if len(typed) != len(want) || typed[0] != want[0] || typed[1] != want[1] {
		t.Errorf("type tokens = %+v, want %+v", typed, want)
	}
}
