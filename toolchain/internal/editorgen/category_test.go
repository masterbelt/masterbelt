package main

import (
	"strings"
	"testing"
)

// allLexCategories is every cold-start category the generator projects, keyed
// by a descriptive name for test failures. Keeping the list here (not in the
// table) means a new category added to the grammar must be added to the test to
// be covered — the coverage grows with the table by hand, deliberately.
var allLexCategories = map[string]lexCategory{
	"keyword":         lexKeyword,
	"comment.doc":     lexCommentDoc,
	"comment.line":    lexCommentLine,
	"comment.block":   lexCommentBlock,
	"number.datetime": lexNumberDatetime,
	"number.duration": lexNumberDuration,
	"number.integer":  lexNumberInteger,
	"string":          lexString,
	"string.escape":   lexStringEscape,
	"operator.range":  lexOperatorRange,
	"operator.assign": lexOperatorAssign,
	"operator":        lexOperator,
}

// TestCategoriesAreCanonical pins the category table's core invariant: every
// cold-start lexCategory names a canonical category — one of the server's
// semantic token types (pkg/belt/lsp/semantic.go). A scope that claimed a
// category the server cannot emit would let the cold-start colour drift from
// the accurate one the server later sends, the exact failure the single source
// is meant to rule out.
func TestCategoriesAreCanonical(t *testing.T) {
	for name, c := range allLexCategories {
		if !canonicalCategories[c.semantic] {
			t.Errorf("%s: semantic category %q is not in the legend's vocabulary", name, c.semantic)
		}
		if c.tmScopeBase == "" {
			t.Errorf("%s: empty TextMate scope base", name)
		}
		if want := "." + langName; !strings.HasSuffix(c.tmScope(), want) {
			t.Errorf("%s: tmScope() = %q, want a %q suffix", name, c.tmScope(), want)
		}
	}
}
