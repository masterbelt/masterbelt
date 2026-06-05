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

func TestSemanticTokensLiterals(t *testing.T) {
	// Datetime and duration literals colour as numbers, one token each —
	// matching their cold-start constant.numeric scopes.
	doc := abstract.NewDocument([]byte("const D = D2009-03-31T23:59:59.000Z\nconst W = 3w4d\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 5, stKeyword, 0},
		{0, 6, 1, stVariable, smDeclaration | smReadonly},
		{0, 8, 1, stOperator, 0},
		{0, 10, 25, stNumber, 0}, // the whole datetime literal
		{1, 0, 5, stKeyword, 0},
		{1, 6, 1, stVariable, smDeclaration | smReadonly},
		{1, 8, 1, stOperator, 0},
		{1, 10, 4, stNumber, 0}, // the whole duration literal
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

func TestSemanticTokensArrowLambda(t *testing.T) {
	// The arrow of an arrow-bodied lambda colours as an operator, like : and =;
	// fn stays a keyword.
	doc := abstract.NewDocument([]byte("const F = fn(x) -> x\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 5, stKeyword, 0},                           // const
		{0, 6, 1, stVariable, smDeclaration | smReadonly}, // F (declared name)
		{0, 8, 1, stOperator, 0},                          // =
		{0, 10, 2, stKeyword, 0},                          // fn
		{0, 13, 1, stParameter, smDeclaration},            // x (declared parameter)
		{0, 16, 2, stOperator, 0},                         // ->
		{0, 19, 1, stVariable, smReadonly},                // x (reference)
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

func TestSemanticTokensWhereClause(t *testing.T) {
	// where colours as a keyword like the rest of the declaration; the
	// comparison operator carries no semantic token (the grammar colours it).
	doc := abstract.NewDocument([]byte("type Port = int32 where self >= 1\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 4, stKeyword, 0},          // type
		{0, 5, 4, stType, smDeclaration}, // Port (declared name)
		{0, 10, 1, stOperator, 0},        // =
		{0, 12, 5, stType, 0},            // int32
		{0, 18, 5, stKeyword, 0},         // where
		{0, 24, 4, stKeyword, 0},         // self
		{0, 32, 1, stNumber, 0},          // 1
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

func TestSemanticTokensSwitch(t *testing.T) {
	// switch colours as a keyword like return; the arrow is an operator, the
	// scrutinee and an arm's bare value read as read-only variables, and the
	// wildcard "_" is just such an identifier.
	doc := abstract.NewDocument([]byte("pub fn c(r: R): string {\n  switch r {\n    A -> return \"a\"\n    _ -> return \"b\"\n  }\n}\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{1, 2, 6, stKeyword, 0},           // switch
		{1, 9, 1, stVariable, smReadonly}, // r (scrutinee)
		{2, 4, 1, stVariable, smReadonly}, // A (arm value)
		{2, 6, 2, stOperator, 0},          // ->
		{2, 9, 6, stKeyword, 0},           // return
		{2, 16, 3, stString, 0},           // "a"
		{3, 4, 1, stVariable, smReadonly}, // _ (wildcard)
		{3, 6, 2, stOperator, 0},          // ->
		{3, 9, 6, stKeyword, 0},           // return
		{3, 16, 3, stString, 0},           // "b"
	}

	// The first eight tokens (through the function header) are checked by the
	// other tests; assert on the switch's body tokens, found by skipping to the
	// switch keyword.
	start := 0
	for i, tk := range got {
		if tk == want[0] {
			start = i
			break
		}
	}
	body := got[start:]
	if len(body) < len(want) {
		t.Fatalf("got %d body tokens, want at least %d:\n%+v", len(body), len(want), body)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Errorf("token[%d] = %+v, want %+v", i, body[i], want[i])
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

func TestSemanticTokensAssert(t *testing.T) {
	// assert is a keyword like const; the condition's names and literals
	// classify as in any other expression.
	doc := abstract.NewDocument([]byte("const X = 1\nassert X > 0\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 5, stKeyword, 0},                           // const
		{0, 6, 1, stVariable, smDeclaration | smReadonly}, // X (declared name)
		{0, 8, 1, stOperator, 0},                          // =
		{0, 10, 1, stNumber, 0},                           // 1
		{1, 0, 6, stKeyword, 0},                           // assert
		{1, 7, 1, stVariable, smReadonly},                 // X (reference)
		{1, 11, 1, stNumber, 0},                           // 0
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

func TestSemanticTokensDeclaredNames(t *testing.T) {
	// The names a declaration introduces classify by what they declare: a
	// type declaration's name (and a generic parameter) as a type, a use
	// declaration's binding as a namespace — and every keyword uniformly as
	// a keyword, so `type` and `use` colour exactly like `const`.
	doc := abstract.NewDocument([]byte("use geo from \"geo.belt\"\ntype Opt<T> = T | null\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 3, stKeyword, 0},               // use
		{0, 4, 3, stNamespace, smDeclaration}, // geo
		{0, 8, 4, stKeyword, 0},               // from
		{0, 13, 10, stString, 0},              // "geo.belt"
		{1, 0, 4, stKeyword, 0},               // type
		{1, 5, 3, stType, smDeclaration},      // Opt
		{1, 9, 1, stType, smDeclaration},      // T (declared parameter)
		{1, 12, 1, stOperator, 0},             // =
		{1, 14, 1, stType, 0},                 // T (use in the body)
		{1, 18, 4, stKeyword, 0},              // null
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

func TestSemanticTokensMembers(t *testing.T) {
	// Record fields, method names, parameters, and member accesses each get
	// their own classification; a reference in a body stays a variable.
	src := "type Rec = {\n" +
		"  id: int8\n" +
		"}\n" +
		"type Lvl = int8 impl {\n" +
		"  pub inc(x: int8): self {\n" +
		"    return self.bump(x)\n" +
		"  }\n" +
		"  get(): int8 {\n" +
		"    return self.id\n" +
		"  }\n" +
		"}\n"
	doc := abstract.NewDocument([]byte(src))
	got := decode(semanticTokens(doc).Data)

	find := func(line, char int) (decodedToken, bool) {
		for _, tok := range got {
			if tok.line == line && tok.char == char {
				return tok, true
			}
		}
		return decodedToken{}, false
	}
	cases := []struct {
		name       string
		line, char int
		tokenType  int
		mods       int
	}{
		{"record field", 1, 2, stProperty, smDeclaration},    // id
		{"method name", 4, 6, stMethod, smDeclaration},       // inc
		{"parameter", 4, 10, stParameter, smDeclaration},     // x
		{"self keyword", 5, 11, stKeyword, 0},                // self
		{"method call", 5, 16, stMethod, 0},                  // bump (the callee)
		{"reference in body", 5, 21, stVariable, smReadonly}, // x
		{"field access", 8, 16, stProperty, 0},               // id (not a call)
	}
	for _, tc := range cases {
		tok, ok := find(tc.line, tc.char)
		if !ok {
			t.Errorf("%s: no token at %d:%d (got %+v)", tc.name, tc.line, tc.char, got)
			continue
		}
		if tok.tokenType != tc.tokenType || tok.mods != tc.mods {
			t.Errorf("%s = %+v, want type %d mods %d", tc.name, tok, tc.tokenType, tc.mods)
		}
	}
}

func TestSemanticTokensErrorConversion(t *testing.T) {
	// A conversion's callee names a type — a resolution fact — and colours
	// as the type it constructs through the program-aware pass.
	doc := testView("const E = error(\"boom\")\nconst M = E.message()\n")
	got := decode(semanticTokensIn(doc).Data)

	find := func(line, char int) (decodedToken, bool) {
		for _, tok := range got {
			if tok.line == line && tok.char == char {
				return tok, true
			}
		}
		return decodedToken{}, false
	}
	cases := []struct {
		name       string
		line, char int
		tokenType  int
		mods       int
	}{
		{"conversion callee", 0, 10, stType, 0},      // error
		{"message", 0, 16, stString, 0},              // "boom"
		{"reference", 1, 10, stVariable, smReadonly}, // E
		{"method call", 1, 12, stMethod, 0},          // message (the callee)
	}
	for _, tc := range cases {
		tok, ok := find(tc.line, tc.char)
		if !ok {
			t.Errorf("%s: no token at %d:%d (got %+v)", tc.name, tc.line, tc.char, got)
			continue
		}
		if tok.tokenType != tc.tokenType || tok.mods != tc.mods {
			t.Errorf("%s = %+v, want type %d mods %d", tc.name, tok, tc.tokenType, tc.mods)
		}
	}
}

func TestSemanticTokensEffects(t *testing.T) {
	// The effect keywords and await colour as keywords, uniformly with the
	// cold-start grammar.
	src := "extern fn io async fetch(url: string): string\n" +
		"pub fn io async page(url: string): string {\n" +
		"  return await fetch(url)\n" +
		"}\n"
	doc := abstract.NewDocument([]byte(src))
	got := decode(semanticTokens(doc).Data)

	find := func(line, char int) (decodedToken, bool) {
		for _, tok := range got {
			if tok.line == line && tok.char == char {
				return tok, true
			}
		}
		return decodedToken{}, false
	}
	cases := []struct {
		name       string
		line, char int
	}{
		{"extern io", 0, 10},
		{"extern async", 0, 13},
		{"fn io", 1, 7},
		{"fn async", 1, 10},
		{"await", 2, 9},
	}
	for _, tc := range cases {
		tok, ok := find(tc.line, tc.char)
		if !ok {
			t.Errorf("%s: no token at %d:%d (got %+v)", tc.name, tc.line, tc.char, got)
			continue
		}
		if tok.tokenType != stKeyword {
			t.Errorf("%s = %+v, want keyword", tc.name, tok)
		}
	}
}
