package lexer

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// TestLexerLossless verifies the stream covers every byte with no gaps or
// overlaps and reproduces the source exactly — the property formatters need.
func TestLexerLossless(t *testing.T) {
	inputs := []string{
		"",
		"   ",
		"\t \r\n  x",
		"const x = 1\n",
		"a\t\tb  c\r\n/* c */  // d\n",
		"  pub  const  X = 0  ",
	}
	for _, src := range inputs {
		file := source.NewFile("x.belt", []byte(src))
		tokens := New(file).Tokens()

		off := 0
		var b strings.Builder
		for _, tok := range tokens {
			if tok.Offset != off {
				t.Fatalf("src %q: token %s starts at %d, want %d (gap or overlap)", src, tok.Kind, tok.Offset, off)
			}
			b.WriteString(tok.Text(file))
			off = tok.End()
		}
		if b.String() != src {
			t.Errorf("src %q: reassembled %q", src, b.String())
		}
		if last := tokens[len(tokens)-1]; last.Kind != token.EOF || last.Offset != len(src) {
			t.Errorf("src %q: last token = %s@%d, want EOF@%d", src, last.Kind, last.Offset, len(src))
		}
	}
}

// TestLexerSpans checks that token byte ranges resolve through the source
// position service to accurate 1-based line/column locations, and that a
// token's width matches both its text and its resolved span length.
func TestLexerSpans(t *testing.T) {
	src := []byte("const MaxLevel: int64 = 100\n")
	file := source.NewFile("inline.belt", src)
	tokens := New(file).Tokens()

	// Locate the "100" literal and assert its start position.
	var hundred token.Token
	for _, tok := range tokens {
		if tok.Kind == token.Int && tok.Text(file) == "100" {
			hundred = tok
		}
	}
	if hundred.Kind != token.Int {
		t.Fatalf("did not find the Int token")
	}
	if got, want := hundred.Span(file).Start, (source.Position{ByteOffset: 24, Line: 1, Column: 25}); got != want {
		t.Errorf("100 start = %+v, want %+v", got, want)
	}

	// Width, text length, and span length must agree for every token.
	for _, tok := range tokens {
		if got, want := tok.Width, len(tok.Text(file)); got != want {
			t.Errorf("%s width = %d, want text len %d", tok, got, want)
		}
		if got := tok.Span(file).Len(); got != tok.Width {
			t.Errorf("%s span len = %d, want width %d", tok, got, tok.Width)
		}
	}
}

func TestLexerDiagnostics(t *testing.T) {
	t.Run("unterminated block comment", func(t *testing.T) {
		file := source.NewFile("t.belt", []byte("const x = 1 /* oops"))
		lex := New(file)
		tokens := lex.Tokens()

		// The dangling comment is still returned as a BlockComment so editors
		// keep highlighting it as a comment.
		last := tokens[len(tokens)-2] // before EOF
		if last.Kind != token.BlockComment || last.Text(file) != "/* oops" {
			t.Errorf("last token = %s %q, want BlockComment %q", last.Kind, last.Text(file), "/* oops")
		}

		diags := lex.Diagnostics()
		if len(diags) != 1 {
			t.Fatalf("Diagnostics() = %v, want exactly one", diags)
		}
		if diags[0].Code != CodeUnterminatedBlockComment {
			t.Errorf("code = %q, want %q", diags[0].Code, CodeUnterminatedBlockComment)
		}
		if got, want := diags[0].Message, "unterminated block comment"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("unterminated string literal", func(t *testing.T) {
		file := source.NewFile("t.belt", []byte(`const x = "oops`))
		lex := New(file)
		tokens := lex.Tokens()

		// The dangling string is still returned as a String token, so editors
		// keep highlighting it while the closing quote is being typed.
		last := tokens[len(tokens)-2] // before EOF
		if last.Kind != token.String || last.Text(file) != `"oops` {
			t.Errorf("last token = %s %q, want String %q", last.Kind, last.Text(file), `"oops`)
		}

		diags := lex.Diagnostics()
		if len(diags) != 1 {
			t.Fatalf("Diagnostics() = %v, want exactly one", diags)
		}
		if diags[0].Code != CodeUnterminatedString {
			t.Errorf("code = %q, want %q", diags[0].Code, CodeUnterminatedString)
		}
		if got, want := diags[0].Message, "unterminated string literal"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("string is not closed across a newline", func(t *testing.T) {
		file := source.NewFile("t.belt", []byte("\"oops\nx"))
		lex := New(file)
		tokens := lex.Tokens()

		// The newline terminates the unterminated string rather than being
		// swallowed: it remains its own token so the stream stays lossless.
		if tokens[0].Kind != token.String || tokens[0].Text(file) != `"oops` {
			t.Errorf("token[0] = %s %q, want String %q", tokens[0].Kind, tokens[0].Text(file), `"oops`)
		}
		if tokens[1].Kind != token.Newline {
			t.Errorf("token[1] = %s, want Newline", tokens[1].Kind)
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 || diags[0].Code != CodeUnterminatedString {
			t.Fatalf("Diagnostics() = %v, want one %q", diags, CodeUnterminatedString)
		}
	})

	t.Run("illegal ascii character", func(t *testing.T) {
		file := source.NewFile("t.belt", []byte("const x = #"))
		lex := New(file)
		tokens := lex.Tokens()

		bad := tokens[len(tokens)-2]
		if bad.Kind != token.Illegal || bad.Text(file) != "#" {
			t.Errorf("token = %s %q, want Illegal %q", bad.Kind, bad.Text(file), "#")
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 {
			t.Fatalf("Diagnostics() = %v, want exactly one", diags)
		}
		if diags[0].Code != CodeUnexpectedCharacter {
			t.Errorf("code = %q, want %q", diags[0].Code, CodeUnexpectedCharacter)
		}
		if got, want := diags[0].Message, "unexpected character: '#'"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
		if got := diags[0].Fields["char"].String(); got != "'#'" {
			t.Errorf("Fields[char] = %q, want %q", got, "'#'")
		}
	})

	t.Run("lone ampersand is illegal", func(t *testing.T) {
		// "&&" is the only use of '&'; a lone one begins no token.
		file := source.NewFile("t.belt", []byte("a & b"))
		lex := New(file)
		tokens := lex.Tokens()

		var amp token.Token
		for _, tok := range tokens {
			if tok.Kind == token.Illegal {
				amp = tok
			}
		}
		if amp.Text(file) != "&" {
			t.Errorf("illegal token = %q, want %q", amp.Text(file), "&")
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 || diags[0].Code != CodeUnexpectedCharacter {
			t.Fatalf("Diagnostics() = %v, want one %q", diags, CodeUnexpectedCharacter)
		}
		if got, want := diags[0].Message, "unexpected character: '&'"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("illegal multibyte rune is one token", func(t *testing.T) {
		// A stray multibyte rune must not fragment into per-byte Illegal tokens.
		file := source.NewFile("t.belt", []byte("あ"))
		lex := New(file)
		tokens := lex.Tokens()

		if len(tokens) != 2 { // Illegal + EOF
			t.Fatalf("got %d tokens, want 2: %v", len(tokens), tokens)
		}
		if tokens[0].Kind != token.Illegal || tokens[0].Width != 3 {
			t.Errorf("token = %s width %d, want Illegal width 3", tokens[0].Kind, tokens[0].Width)
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 || diags[0].Code != CodeUnexpectedCharacter {
			t.Fatalf("Diagnostics() = %v, want one %q", diags, CodeUnexpectedCharacter)
		}
		if got, want := diags[0].Message, "unexpected character: 'あ'"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})
}

// TestLexerOperators checks each operator and boolean keyword, including the
// maximal-munch boundaries where a one-byte operator and a two-byte operator
// share a leading byte.
func TestLexerOperators(t *testing.T) {
	cases := []struct {
		src  string
		kind token.Kind
	}{
		{"+", token.Plus}, {"-", token.Minus}, {"->", token.Arrow}, {"*", token.Star},
		{"/", token.Slash}, {"%", token.Percent},
		{"=", token.Assign}, {"==", token.EqEq},
		{"!", token.Bang}, {"!=", token.BangEq},
		{"<", token.Lt}, {"<=", token.LtEq},
		{">", token.Gt}, {">=", token.GtEq},
		{"&&", token.AmpAmp}, {"||", token.PipePipe}, {"|", token.Pipe},
		{"(", token.LParen}, {")", token.RParen},
		{"{", token.LBrace}, {"}", token.RBrace},
		{"[", token.LBracket}, {"]", token.RBracket},
		{",", token.Comma}, {".", token.Dot},
		{"true", token.True}, {"false", token.False},
	}
	for _, c := range cases {
		file := source.NewFile("op.belt", []byte(c.src))
		tokens := New(file).Tokens()
		if len(tokens) != 2 { // operator + EOF
			t.Errorf("%q: got %d tokens, want 2: %v", c.src, len(tokens), tokens)
			continue
		}
		if tokens[0].Kind != c.kind || tokens[0].Text(file) != c.src {
			t.Errorf("%q: got %s %q, want %s", c.src, tokens[0].Kind, tokens[0].Text(file), c.kind)
		}
	}

	// Maximal munch must not glue separated operators together.
	t.Run("separated equals are two assigns", func(t *testing.T) {
		file := source.NewFile("op.belt", []byte("= ="))
		tokens := New(file).Tokens()
		if len(tokens) != 4 || tokens[0].Kind != token.Assign || tokens[2].Kind != token.Assign {
			t.Errorf("%q tokenized as %v, want Assign Whitespace Assign EOF", "= =", tokens)
		}
	})

	// "->" is one token, not Minus then Gt — and "- >" stays two tokens.
	t.Run("arrow is one token", func(t *testing.T) {
		file := source.NewFile("op.belt", []byte("a->b"))
		tokens := New(file).Tokens()
		var arrow token.Token
		for _, tok := range tokens {
			if tok.Kind == token.Arrow {
				arrow = tok
			}
		}
		if arrow.Text(file) != "->" {
			t.Errorf("got %v, want a single Arrow token", tokens)
		}
	})
	t.Run("separated minus gt are two tokens", func(t *testing.T) {
		file := source.NewFile("op.belt", []byte("- >"))
		tokens := New(file).Tokens()
		if len(tokens) != 4 || tokens[0].Kind != token.Minus || tokens[2].Kind != token.Gt {
			t.Errorf("%q tokenized as %v, want Minus Whitespace Gt EOF", "- >", tokens)
		}
	})

	// "<=" is one token, not Lt then Assign.
	t.Run("le is one token", func(t *testing.T) {
		file := source.NewFile("op.belt", []byte("a<=b"))
		tokens := New(file).Tokens()
		var le token.Token
		for _, tok := range tokens {
			if tok.Kind == token.LtEq {
				le = tok
			}
		}
		if le.Text(file) != "<=" {
			t.Errorf("got %v, want a single LtEq token", tokens)
		}
	})
}

// TestLexerStrings checks that a well-formed double-quoted string is one String
// token whose text includes the quotes, covering every recognized escape, the
// \u{...} unicode escape, and raw multi-byte UTF-8 — none of which produces a
// diagnostic.
func TestLexerStrings(t *testing.T) {
	cases := []string{
		`""`,
		`"label"`,
		`"a b\tc"`,
		`"say \"hi\""`,            // an escaped quote stays inside the string
		`"trailing backslash \\"`, // an escaped backslash
		`"\n\r\t\0"`,              // the simple escapes
		`"\u{41}"`,                // a unicode escape, one digit short of the max width
		`"\u{1F389}"`,             // a six-digit unicode escape (an emoji)
		`"日本語 🎉"`,                 // raw multi-byte UTF-8 carried through verbatim
	}
	for _, src := range cases {
		file := source.NewFile("s.belt", []byte(src))
		lex := New(file)
		tokens := lex.Tokens()
		if len(tokens) != 2 { // String + EOF
			t.Errorf("%q: got %d tokens, want 2: %v", src, len(tokens), tokens)
			continue
		}
		if tokens[0].Kind != token.String || tokens[0].Text(file) != src {
			t.Errorf("%q: got %s %q, want String %q", src, tokens[0].Kind, tokens[0].Text(file), src)
		}
		if diags := lex.Diagnostics(); len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics %v", src, diags)
		}
	}
}

// TestLexerStringEscapeDiagnostics checks that a malformed escape is reported
// while the string is still returned as one lossless String token.
func TestLexerStringEscapeDiagnostics(t *testing.T) {
	cases := []struct {
		src  string
		code diagnostic.Code
		msg  string
	}{
		{`"\q"`, CodeInvalidEscape, `invalid escape sequence: \q`},
		{"\"\\uABCD\"", CodeInvalidUnicodeEscape, `invalid unicode escape: \u`},          // \u must be braced
		{`"\u{}"`, CodeInvalidUnicodeEscape, `invalid unicode escape: \u{}`},             // no digits
		{`"\u{110000}"`, CodeInvalidUnicodeEscape, `invalid unicode escape: \u{110000}`}, // beyond 0x10FFFF
		{`"\u{D800}"`, CodeInvalidUnicodeEscape, `invalid unicode escape: \u{D800}`},     // a surrogate
		{`"\u{XYZ}"`, CodeInvalidUnicodeEscape, `invalid unicode escape: \u{`},           // not hex
	}
	for _, c := range cases {
		file := source.NewFile("s.belt", []byte(c.src))
		lex := New(file)
		tokens := lex.Tokens()
		if tokens[0].Kind != token.String || tokens[0].Text(file) != c.src {
			t.Errorf("%q: token = %s %q, want a lossless String", c.src, tokens[0].Kind, tokens[0].Text(file))
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 {
			t.Fatalf("%q: Diagnostics() = %v, want exactly one", c.src, diags)
		}
		if diags[0].Code != c.code {
			t.Errorf("%q: code = %q, want %q", c.src, diags[0].Code, c.code)
		}
		if got := diags[0].Message; got != c.msg {
			t.Errorf("%q: message = %q, want %q", c.src, got, c.msg)
		}
	}
}

func TestLexerEOFIsIdempotent(t *testing.T) {
	file := source.NewFile("empty.belt", nil)
	l := New(file)

	for i := range 3 {
		if tok := l.Next(); tok.Kind != token.EOF {
			t.Fatalf("Next() #%d = %s, want EOF", i, tok)
		}
	}
}
