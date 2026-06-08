package formatter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolve checks masterbelt's consumption of an .editorconfig: that the
// resolved section maps onto a Layout, and that each field a config leaves
// unset (or a config that does not match the file) falls back. The parser
// itself is the library's responsibility, vetted by the EditorConfig core test
// suite, so it is not retested here — only the translation is.
func TestResolve(t *testing.T) {
	// A deliberately unmistakable fallback, so any field that falls through to
	// it is obvious in a failure.
	fallback := Layout{Indent: "<indent>", EndOfLine: "<eol>"}

	cases := []struct {
		name   string
		config string
		want   Layout
	}{
		{
			name:   "space two lf",
			config: "root = true\n[*.belt]\nindent_style = space\nindent_size = 2\nend_of_line = lf\n",
			want:   Layout{Indent: "  ", EndOfLine: "\n"},
		},
		{
			name:   "tab crlf",
			config: "root = true\n[*.belt]\nindent_style = tab\nend_of_line = crlf\n",
			want:   Layout{Indent: "\t", EndOfLine: "\r\n"},
		},
		{
			name:   "space four cr",
			config: "root = true\n[*.belt]\nindent_style = space\nindent_size = 4\nend_of_line = cr\n",
			want:   Layout{Indent: "    ", EndOfLine: "\r"},
		},
		{
			name:   "eol unset falls back",
			config: "root = true\n[*.belt]\nindent_style = space\nindent_size = 3\n",
			want:   Layout{Indent: "   ", EndOfLine: fallback.EndOfLine},
		},
		{
			name:   "indent unset falls back",
			config: "root = true\n[*.belt]\nend_of_line = lf\n",
			want:   Layout{Indent: fallback.Indent, EndOfLine: "\n"},
		},
		{
			name:   "section does not match",
			config: "root = true\n[*.go]\nindent_style = tab\nend_of_line = crlf\n",
			want:   fallback,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".editorconfig"), []byte(c.config), 0o644); err != nil {
				t.Fatal(err)
			}
			got := Resolve(filepath.Join(dir, "foo.belt"), fallback)
			if got != c.want {
				t.Errorf("Resolve = %#v, want %#v", got, c.want)
			}
		})
	}
}

// TestResolveNoConfig pins that a path with no .editorconfig (a root=true file
// that matches nothing above the file's directory bounds the search) resolves
// to the fallback untouched.
func TestResolveNoConfig(t *testing.T) {
	dir := t.TempDir()
	// A root marker that matches no section stops the walk at dir, so nothing
	// above the temp directory can leak in.
	if err := os.WriteFile(filepath.Join(dir, ".editorconfig"), []byte("root = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallback := Layout{Indent: "  ", EndOfLine: "\n"}
	if got := Resolve(filepath.Join(dir, "sub", "foo.belt"), fallback); got != fallback {
		t.Errorf("Resolve = %#v, want fallback %#v", got, fallback)
	}
}
