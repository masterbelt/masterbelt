package abstract

import (
	"math/rand"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
)

func naiveSplice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}

// assertMatchesFreshLower is the oracle: the incrementally maintained AST must
// dump identically to lowering the current content from scratch.
func assertMatchesFreshLower(t *testing.T, d *Document, content []byte) {
	t.Helper()
	fresh, _ := Lower(content)
	got, want := ast.Dump(d.File()), ast.Dump(fresh)
	if got != want {
		t.Fatalf("AST mismatch (content %q)\n--- got ---\n%s--- want ---\n%s", content, got, want)
	}
}

func TestDocumentScriptedEdits(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		edit    source.Edit
	}{
		{"rename in value", "const x = 1\n", source.Edit{Start: 10, End: 11, NewText: []byte("9")}},
		{"add pub", "const x = 1\nconst y = 2\n", source.Edit{Start: 12, End: 12, NewText: []byte("pub ")}},
		{"add type annotation", "const x = 1\n", source.Edit{Start: 7, End: 7, NewText: []byte(": int64")}},
		{"insert a decl", "const x = 1\nconst z = 3\n", source.Edit{Start: 12, End: 12, NewText: []byte("const y = 2\n")}},
		{"add doc comment", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("/// hi\n")}},
		{"change value to name ref", "const x = 1\n", source.Edit{Start: 10, End: 11, NewText: []byte("y")}},
		{"remove initializer", "const x = 1\n", source.Edit{Start: 7, End: 11, NewText: nil}},
		{"join two decls", "const x = 1\nconst y = 2\n", source.Edit{Start: 11, End: 12, NewText: nil}},
		{"delete everything", "const x = 1\n", source.Edit{Start: 0, End: 12, NewText: nil}},
		{"introduce junk", "const x = 1\n", source.Edit{Start: 0, End: 0, NewText: []byte("= =\n")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDocument([]byte(tc.initial))
			content := naiveSplice([]byte(tc.initial), tc.edit.Start, tc.edit.End, tc.edit.NewText)
			d.Edit(tc.edit)
			assertMatchesFreshLower(t, d, content)
		})
	}
}

func TestDocumentReusesUneditedDecls(t *testing.T) {
	// Editing the third declaration must leave the AST nodes of the first two
	// reused by identity (the green CST nodes survived, so the cache hit).
	d := NewDocument([]byte("const a = 1\nconst b = 2\nconst c = 3\n"))
	a, b := d.File().Decls[0], d.File().Decls[1]

	d.Edit(source.Edit{Start: 34, End: 35, NewText: []byte("9")}) // c's value 3 -> 9
	assertMatchesFreshLower(t, d, []byte("const a = 1\nconst b = 2\nconst c = 9\n"))

	if d.File().Decls[0] != a || d.File().Decls[1] != b {
		t.Fatal("unedited declarations were re-lowered; expected them to be reused")
	}
	if v, ok := d.File().Decls[2].Value.(*ast.IntLit); !ok || v.Text != "9" {
		t.Fatalf("edited decl Value = %+v, want IntLit 9", d.File().Decls[2].Value)
	}
}

func TestDocumentSequentialEdits(t *testing.T) {
	d := NewDocument(nil)
	var content []byte

	typed := "/// doc\npub const Answer: int64 = 42\nconst Mirror = Answer\n"
	for i := 0; i < len(typed); i++ {
		e := source.Edit{Start: len(content), End: len(content), NewText: []byte{typed[i]}}
		content = naiveSplice(content, e.Start, e.End, e.NewText)
		d.Edit(e)
		assertMatchesFreshLower(t, d, content)
	}
}

func TestDocumentFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(0xA57))
	alphabet := []string{
		"a", "Z", "x", "0", "9", " ", "\n", ":", "=", "あ",
		"const ", "pub ", "/// d\n", "// c\n", "int64", "MaxLevel",
	}

	start := "const x = 0\n"
	d := NewDocument([]byte(start))
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
		d.Edit(edit)
		assertMatchesFreshLower(t, d, content)
	}
}
