package concrete

import (
	"math/rand"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
)

// naiveSplice is the obvious reference for applying an edit to bytes.
func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// assertMatchesFullParse is the oracle: the incrementally maintained tree and
// diagnostics must be identical to parsing the current content from scratch.
func assertMatchesFullParse(t *testing.T, d *Document, content []byte) {
	t.Helper()

	oracleRoot, oracleDiags := Parse(content)

	if !cst.Equal(d.Root(), oracleRoot) {
		t.Fatalf("tree mismatch (content %q)\n--- got ---\n%s--- want ---\n%s",
			content, cst.Sprint(d.Buffer(), d.Root()), cst.Sprint(source.NewFile("", content), oracleRoot))
	}

	// Losslessness: the leaves must still reproduce the source.
	if got := leafText(d.Buffer(), d.Root()); got != string(content) {
		t.Fatalf("tree not lossless after edit\n content: %q\n leaves:  %q", content, got)
	}

	got, want := d.Diagnostics(), oracleDiags
	if len(got) != len(want) {
		t.Fatalf("diagnostic count = %d, want %d (content %q)\n got:  %v\n want: %v",
			len(got), len(want), content, got, want)
	}
	for i := range want {
		if got[i].Code != want[i].Code || got[i].Severity != want[i].Severity ||
			got[i].Offset != want[i].Offset || got[i].Width != want[i].Width ||
			got[i].Message != want[i].Message {
			t.Fatalf("diagnostic[%d] = %+v, want %+v (content %q)", i, got[i], want[i], content)
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
		{"append digit merging int", "const x = 1", source.Edit{Start: 11, End: 11, NewText: []byte("2")}},
		{"delete the initializer value", "const x = 100", source.Edit{Start: 10, End: 13, NewText: nil}},
		{"add a type annotation", "const x = 1", source.Edit{Start: 7, End: 7, NewText: []byte(": int64")}},
		{"make a decl public", "const x = 1\nconst y = 2\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub ")}},
		{"insert a whole decl between", "const x = 1\nconst y = 2\n", source.Edit{Start: 12, End: 12, NewText: []byte("const z = 3\n")}},
		{"join two decls by deleting newline", "const x = 1\nconst y = 2\n", source.Edit{Start: 11, End: 12, NewText: nil}},
		{"break const keyword", "const x = 1\n", source.Edit{Start: 2, End: 2, NewText: []byte(" ")}},
		{"edit first of three decls", "const a = 1\nconst b = 2\nconst c = 3\n", source.Edit{Start: 6, End: 7, NewText: []byte("A")}},
		{"edit last of three decls", "const a = 1\nconst b = 2\nconst c = 3\n", source.Edit{Start: 30, End: 31, NewText: []byte("C")}},
		{"insert doc comment", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("/// doc\n")}},
		{"append at end", "const x = 1\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub const y = 2\n")}},
		{"edit at very start", "const x = 1", source.Edit{Start: 0, End: 0, NewText: []byte("pub ")}},
		{"delete everything", "const x = 1\nconst y = 2\n", source.Edit{Start: 0, End: 24, NewText: nil}},
		{"introduce a syntax error", "const x = 1\n", source.Edit{Start: 8, End: 9, NewText: nil}}, // remove '='
		{"open block comment swallowing decls", "const x = 1\nconst y = 2\n", source.Edit{Start: 0, End: 0, NewText: []byte("/*")}},
		{"stray operator line", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("= =\n")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := NewDocument([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			doc.Edit(tc.edit)
			assertMatchesFullParse(t, doc, content)
		})
	}
}

func TestDocumentSequentialEdits(t *testing.T) {
	// Type a declaration one character at a time, then delete it from the front.
	doc := NewDocument(nil)
	var content []byte

	typed := "/// doc\npub const Answer: int64 = 42\n"
	for i := 0; i < len(typed); i++ {
		e := source.Edit{Start: len(content), End: len(content), NewText: []byte{typed[i]}}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullParse(t, doc, content)
	}

	for len(content) > 0 {
		e := source.Edit{Start: 0, End: 1, NewText: nil}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		doc.Edit(e)
		assertMatchesFullParse(t, doc, content)
	}
}

func TestDocumentReusesUneditedDecls(t *testing.T) {
	// Editing the last declaration must leave the green node of the first
	// untouched (same pointer), proving the subtree was reused, not rebuilt.
	doc := NewDocument([]byte("const a = 1\nconst b = 2\n"))
	first := doc.Root().Children()[0]

	doc.Edit(source.Edit{Start: 22, End: 23, NewText: []byte("9")}) // b's value 2 -> 9
	assertMatchesFullParse(t, doc, []byte("const a = 1\nconst b = 9\n"))

	if doc.Root().Children()[0] != first {
		t.Fatal("first declaration was rebuilt; expected it to be reused")
	}
}

func TestDocumentFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0xB317))
	alphabet := []string{
		"a", "Z", "x", "0", "9", " ", "\n", "/", "*", ":", "=", "あ",
		"const ", "pub ", "// c\n", "/* b */", "int64",
	}

	start := "const x = 0\n"
	doc := NewDocument([]byte(start))
	content := []byte(start)

	for range 2000 {
		s := r.Intn(len(content) + 1)
		e := s + r.Intn(len(content)-s+1)

		var repl []byte
		for n := r.Intn(6); n > 0; n-- {
			repl = append(repl, alphabet[r.Intn(len(alphabet))]...)
		}

		edit := source.Edit{Start: s, End: e, NewText: repl}
		content = naiveSplice(content, s, e, repl)
		doc.Edit(edit)
		assertMatchesFullParse(t, doc, content)
	}
}
