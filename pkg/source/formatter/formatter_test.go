package formatter

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

func TestFormat(t *testing.T) {
	src := "ab  \n\n"
	buf := source.NewFile("", []byte(src))
	// A realistic leaf tiling of "ab  \n\n": the identifier, its trailing
	// spaces, then two newlines. Format trims the trailing spaces and the extra
	// blank line down to a single final newline.
	root := cst.NewNode(cst.File, []cst.Green{
		cst.NewToken(token.Ident, "ab"),
		cst.NewToken(token.Whitespace, "  "),
		cst.NewToken(token.Newline, "\n"),
		cst.NewToken(token.Newline, "\n"),
	})

	if got := Format(buf, root, DefaultLayout); got != "ab\n" {
		t.Errorf("Format = %q, want %q", got, "ab\n")
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"a":         "a\n",
		"a\n":       "a\n",
		"a   \n":    "a\n",
		"a\n\n\n":   "a\n",
		"a\t\nb \n": "a\nb\n",
	}
	for in, want := range cases {
		if got := normalize(in, DefaultLayout); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeBlankLines pins blank-line collapsing: a run of blank lines
// becomes a single blank line, and leading and trailing blanks vanish.
func TestNormalizeBlankLines(t *testing.T) {
	cases := map[string]string{
		"a\n\n\n\nb\n":    "a\n\nb\n",      // a long gap collapses to one blank line
		"a\n\nb\n":        "a\n\nb\n",      // a single blank line is kept
		"a\nb\n":          "a\nb\n",        // no blank line stays none
		"\n\n\na\n":       "a\n",           // leading blanks vanish
		"a\n\n\n":         "a\n",           // trailing blanks vanish
		"a\n\nb\n\n\nc\n": "a\n\nb\n\nc\n", // each gap independently collapses
		"a\n  \n\nb\n":    "a\n\nb\n",      // a whitespace-only line counts as blank
	}
	for in, want := range cases {
		if got := normalize(in, DefaultLayout); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeEndOfLine pins the one substrate property normalize draws today:
// every line break renders as the layout's terminator, whatever the input used.
func TestNormalizeEndOfLine(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		layout Layout
		want   string
	}{
		{"lf keeps lf", "a\nb\n", Layout{EndOfLine: "\n"}, "a\nb\n"},
		{"crlf folds to lf", "a\r\nb\r\n", Layout{EndOfLine: "\n"}, "a\nb\n"},
		{"lf becomes crlf", "a\nb\n", Layout{EndOfLine: "\r\n"}, "a\r\nb\r\n"},
		{"crlf stays crlf", "a\r\nb\r\n", Layout{EndOfLine: "\r\n"}, "a\r\nb\r\n"},
		{"lf becomes cr", "a\nb\n", Layout{EndOfLine: "\r"}, "a\rb\r"},
		{"empty stays empty", "", Layout{EndOfLine: "\r\n"}, ""},
	}
	for _, c := range cases {
		if got := normalize(c.in, c.layout); got != c.want {
			t.Errorf("%s: normalize(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
