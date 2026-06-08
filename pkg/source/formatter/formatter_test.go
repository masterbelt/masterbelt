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
	root := cst.NewNode(cst.File, []cst.Green{cst.NewToken(token.Whitespace, src)})

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
