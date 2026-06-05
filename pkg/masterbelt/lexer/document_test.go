package lexer

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// naiveSplice is the obvious reference for applying an edit to bytes.
func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// formatTokens renders a token stream for failure messages.
func formatTokens(toks []token.Token) string {
	var b strings.Builder
	for _, t := range toks {
		fmt.Fprintf(&b, "%s ", t)
	}
	return b.String()
}

// assertMatchesFullLex is the oracle: the incrementally maintained token stream
// and diagnostics must be identical to lexing the current content from scratch.
func assertMatchesFullLex(t *testing.T, doc *Document, content []byte) {
	t.Helper()

	oracle := NewDocument(content)

	got, want := doc.Tokens(), oracle.Tokens()
	if len(got) != len(want) {
		t.Fatalf("token count = %d, want %d\n got:  %s\n want: %s\n content: %q",
			len(got), len(want), formatTokens(got), formatTokens(want), content)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token[%d] = %s, want %s (content %q)\n got:  %s\n want: %s",
				i, got[i], want[i], content, formatTokens(got), formatTokens(want))
		}
	}

	gotD, wantD := doc.Diagnostics(), oracle.Diagnostics()
	if len(gotD) != len(wantD) {
		t.Fatalf("diagnostic count = %d, want %d (content %q)\n got:  %v\n want: %v",
			len(gotD), len(wantD), content, gotD, wantD)
	}
	for i := range wantD {
		if gotD[i].Code != wantD[i].Code || gotD[i].Severity != wantD[i].Severity ||
			gotD[i].Offset != wantD[i].Offset || gotD[i].Width != wantD[i].Width ||
			gotD[i].Message != wantD[i].Message {
			t.Fatalf("diagnostic[%d] = %+v, want %+v (content %q)", i, gotD[i], wantD[i], content)
		}
	}
}

func TestDocumentScriptedEdits(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		edit    source.Edit
	}{
		{"insert inside identifier", "const MaxLevel = 1", source.Edit{Start: 9, End: 9, NewText: []byte("X")}},
		{"delete a token", "const x = 100", source.Edit{Start: 9, End: 13, NewText: nil}},
		{"merge two identifiers", "const ab cd", source.Edit{Start: 8, End: 9, NewText: nil}},
		{"edit inside block comment", "/* hello */ const x = 1", source.Edit{Start: 4, End: 4, NewText: []byte("XYZ ")}},
		{"open a line comment", "a / b\nc", source.Edit{Start: 3, End: 3, NewText: []byte("/")}},
		{"insert a newline", "const x = 1 const y = 2", source.Edit{Start: 11, End: 12, NewText: []byte("\n")}},
		{"append at end", "const x = 1\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub const y = 2\n")}},
		{"edit at very start", "const x = 1", source.Edit{Start: 0, End: 0, NewText: []byte("pub ")}},
		{"insert long run forcing window growth", "a b c d e f g h", source.Edit{Start: 1, End: 1, NewText: []byte(" 1234567890 const verylongidentifier")}},
		{"delete everything", "const x = 1", source.Edit{Start: 0, End: 11, NewText: nil}},
		{"turn slashstar into doc comment", "// x\nconst y = 2", source.Edit{Start: 2, End: 2, NewText: []byte("/")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			doc.Edit(tc.edit)
			assertMatchesFullLex(t, doc, content)
		})
	}
}

func TestDocumentWindowGrowth(t *testing.T) {
	// Opening an unterminated block comment near the start of a long file makes
	// the comment swallow everything to EOF, forcing the relex window to grow
	// (and truncate) repeatedly until it reaches the end.
	initial := []byte(strings.Repeat("const x = 1\n", 40))
	doc := NewDocument(initial)

	edit := source.Edit{Start: 0, End: 0, NewText: []byte("/*")}
	content := naiveSplice(initial, 0, 0, edit.NewText)
	doc.Edit(edit)
	assertMatchesFullLex(t, doc, content)

	// Closing it again should collapse back to the original token shape.
	closeEdit := source.Edit{Start: len(content), End: len(content), NewText: []byte("*/")}
	content = naiveSplice(content, closeEdit.Start, closeEdit.End, closeEdit.NewText)
	doc.Edit(closeEdit)
	assertMatchesFullLex(t, doc, content)
}

func TestDocumentTruncatedTokenAtWindowEdge(t *testing.T) {
	// Splitting a long identifier near its start leaves a long identifier
	// beginning exactly at the changed region, with nothing alignable before it.
	// It overruns the initial relex window, so the freshly lexed token is
	// truncated at the edge and the window must grow before it realigns.
	initial := []byte("x" + strings.Repeat("a", 100) + "\n")
	doc := NewDocument(initial)

	edit := source.Edit{Start: 1, End: 1, NewText: []byte(" ")} // x|aaa... -> x aaa...
	content := naiveSplice(initial, edit.Start, edit.End, edit.NewText)
	doc.Edit(edit)
	assertMatchesFullLex(t, doc, content)
}

func TestDocumentSequentialEdits(t *testing.T) {
	// Type "pub const Answer = 42\n" one character at a time, then delete it.
	doc := NewDocument(nil)
	content := []byte(nil)

	typed := "pub const Answer = 42\n"
	for i := 0; i < len(typed); i++ {
		e := source.Edit{Start: len(content), End: len(content), NewText: []byte{typed[i]}}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullLex(t, doc, content)
	}

	// Now delete from the front, one byte at a time.
	for len(content) > 0 {
		e := source.Edit{Start: 0, End: 1, NewText: nil}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullLex(t, doc, content)
	}
}

func TestDocumentTokenTextAfterEdit(t *testing.T) {
	doc := NewDocument([]byte("const Max = 1"))
	doc.Edit(source.Edit{Start: 9, End: 9, NewText: []byte("Level")}) // Max -> MaxLevel

	var ident token.Token
	for _, tok := range doc.Tokens() {
		if tok.Kind == token.Ident {
			ident = tok
			break
		}
	}
	if got := ident.Text(doc.Buffer()); got != "MaxLevel" {
		t.Errorf("ident text = %q, want %q", got, "MaxLevel")
	}
	if got := ident.Span(doc.Buffer()).Start.Column; got != 7 {
		t.Errorf("ident start column = %d, want 7", got)
	}
}

// TestDocumentLiteralMerges types the multi-token-fusing literals through
// their dangerous edits: a datetime commits on a T typed long after its D, a
// duration extends across a previously separate integer, and deleting a space
// fuses an integer with a unit. Each edit must leave the stream identical to
// a full lex (the window has to back up across the abutting run).
func TestDocumentLiteralMerges(t *testing.T) {
	cases := []struct {
		name  string
		start string
		edit  func(content []byte) source.Edit
	}{
		{
			// D2009-03-31 lexes as five tokens until the clock arrives.
			"typing the clock commits the datetime",
			"const a = D2009-03-31\n",
			func(c []byte) source.Edit {
				at := strings.Index(string(c), "31") + 2
				return source.Edit{Start: at, End: at, NewText: []byte("T23:59:59.000Z")}
			},
		},
		{
			// 3w 4 is a duration then an integer; the d fuses them.
			"typing a unit extends the duration left",
			"const a = 3w4\n",
			func(c []byte) source.Edit {
				at := strings.Index(string(c), "3w4") + 3
				return source.Edit{Start: at, End: at, NewText: []byte("d")}
			},
		},
		{
			// 100 m is an integer and a name; removing the space makes 100m.
			"deleting the space fuses integer and unit",
			"const a = 100 m\n",
			func(c []byte) source.Edit {
				at := strings.Index(string(c), "100 m") + 3
				return source.Edit{Start: at, End: at + 1, NewText: nil}
			},
		},
		{
			// Breaking a datetime apart again: deleting the T shatters it.
			"deleting the T shatters the datetime",
			"const a = D2009-03-31T23:59:59.000Z\n",
			func(c []byte) source.Edit {
				at := strings.Index(string(c), "T23")
				return source.Edit{Start: at, End: at + 1, NewText: nil}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := []byte(c.start)
			doc := NewDocument(content)
			e := c.edit(content)
			content = naiveSplice(content, e.Start, e.End, e.NewText)
			doc.Edit(e)
			assertMatchesFullLex(t, doc, content)
		})
	}
}

func TestDocumentFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0x5EED))
	alphabet := []string{
		"a", "Z", "x", "0", "9", " ", "\n", "/", "*", ":", "=", "あ",
		"const ", "pub ", "// c\n", "/* b */",
		// Operators and the boolean keywords. Single "&"/"|" and the bytes that
		// the two-byte operators share ("=", "!", "<", ">") exercise the
		// maximal-munch merge/split paths in the incremental relexer.
		"+", "-", "%", "!", "<", ">", "&", "|", "==", "!=", "<=", ">=", "&&", "||",
		"true ", "false ",
		// Datetime and duration ingredients: the D/T/Z markers, unit letters,
		// and digit clusters exercise the literal merge/shatter paths (a "."
		// or ":" insertion can fuse or break a committed datetime).
		"D", "T", ".", "w", "d", "h", "m", "s", "ms",
		"D2009-03-31T23:59:59.000Z", "D2009-03-31", "00", "3w4d", "5s", "1h",
	}

	doc := NewDocument([]byte("const x = 0\n"))
	content := []byte("const x = 0\n")

	for range 1500 {
		start := r.Intn(len(content) + 1)
		end := start + r.Intn(len(content)-start+1)

		var repl []byte
		for n := r.Intn(6); n > 0; n-- {
			repl = append(repl, alphabet[r.Intn(len(alphabet))]...)
		}

		e := source.Edit{Start: start, End: end, NewText: repl}
		content = naiveSplice(content, start, end, repl)
		doc.Edit(e)
		assertMatchesFullLex(t, doc, content)
	}
}
