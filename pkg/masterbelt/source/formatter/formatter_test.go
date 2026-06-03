package formatter

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

func TestFormat(t *testing.T) {
	src := "ab  \n\n"
	buf := source.NewFile("", []byte(src))
	root := cst.NewNode(cst.File, []cst.Green{cst.NewToken(token.Whitespace, len(src))})

	if got := Format(buf, root); got != "ab\n" {
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
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
