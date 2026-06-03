package lexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

func TestLexerExampleConst(t *testing.T) {
	path := filepath.Join("..", "testdata", "examples", "0001-const.belt")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}

	file := source.NewFile(path, src)
	lex := New(file)
	got := lex.Tokens()

	want := []struct {
		kind    token.Kind
		literal string
	}{
		{token.BlockComment, "/* this is comment block here */"},
		{token.Newline, "\n"},
		{token.Newline, "\n"}, // blank line
		{token.DocComment, "/// DocComment for MaxLevel"},
		{token.Newline, "\n"},
		{token.Const, "const"},
		{token.Ident, "MaxLevel"},
		{token.Colon, ":"},
		{token.Ident, "int64"},
		{token.Assign, "="},
		{token.Int, "100"},
		{token.Newline, "\n"},
		{token.Newline, "\n"}, // blank line
		{token.DocComment, "/// DocComment for MinLevel"},
		{token.Newline, "\n"},
		{token.DocComment, "/// with multiline."},
		{token.Newline, "\n"},
		{token.Const, "const"},
		{token.Ident, "MinLevel"},
		{token.Assign, "="},
		{token.Int, "0"},
		{token.LineComment, "// type inference"},
		{token.Newline, "\n"},
		{token.Newline, "\n"}, // blank line
		{token.Pub, "pub"},
		{token.Const, "const"},
		{token.Ident, "PublishedMaxLevel"},
		{token.Assign, "="},
		{token.Ident, "MaxLevel"},
		{token.Newline, "\n"},
		{token.EOF, ""},
	}

	if len(got) != len(want) {
		t.Fatalf("token count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Text(file) != w.literal {
			t.Errorf("token[%d] = %s %q, want %s(%q)", i, got[i].Kind, got[i].Text(file), w.kind, w.literal)
		}
	}

	// A well-formed file produces no diagnostics.
	if d := lex.Diagnostics(); len(d) != 0 {
		t.Errorf("Diagnostics() = %v, want none", d)
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

	t.Run("lone slash is illegal", func(t *testing.T) {
		file := source.NewFile("t.belt", []byte("a / b"))
		lex := New(file)
		tokens := lex.Tokens()

		var slash token.Token
		for _, tok := range tokens {
			if tok.Kind == token.Illegal {
				slash = tok
			}
		}
		if slash.Text(file) != "/" {
			t.Errorf("illegal token = %q, want %q", slash.Text(file), "/")
		}
		diags := lex.Diagnostics()
		if len(diags) != 1 || diags[0].Code != CodeUnexpectedCharacter {
			t.Fatalf("Diagnostics() = %v, want one %q", diags, CodeUnexpectedCharacter)
		}
		if got, want := diags[0].Message, "unexpected character: '/'"; got != want {
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

func TestLexerEOFIsIdempotent(t *testing.T) {
	file := source.NewFile("empty.belt", nil)
	l := New(file)

	for i := range 3 {
		if tok := l.Next(); tok.Kind != token.EOF {
			t.Fatalf("Next() #%d = %s, want EOF", i, tok)
		}
	}
}
