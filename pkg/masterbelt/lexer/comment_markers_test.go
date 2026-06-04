package lexer

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// TestCommentMarkersMatchLexer pins the comment-marker constants (which the
// editor-config generator reads) to the lexer's actual scanning, so the two
// cannot silently diverge.
func TestCommentMarkersMatchLexer(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want token.Kind
	}{
		{"line", token.LineCommentPrefix + " x", token.LineComment},
		{"doc", token.DocCommentPrefix + " x", token.DocComment},
		{"block", token.BlockCommentOpen + " x " + token.BlockCommentClose, token.BlockComment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toks := NewDocument([]byte(tc.src)).Tokens()
			if len(toks) == 0 || toks[0].Kind != tc.want {
				t.Fatalf("first token of %q = %v, want %v", tc.src, toks, tc.want)
			}
		})
	}
}
