package beltgen

import (
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// Documents parses a generated project into editable syntax documents, keyed by
// file id. It is the bridge from the raw sources Project emits to the inputs the
// semantic engine consumes — the same parse the project layer performs on disk,
// done in memory so a bench needs no temp directory.
func Documents(srcs map[string][]byte) map[string]*abstract.Document {
	docs := make(map[string]*abstract.Document, len(srcs))
	for id, src := range srcs {
		docs[id] = abstract.NewDocument(src)
	}
	return docs
}

// Uses resolves every file's use declarations to the file id each names. The
// generated layout is flat — a use path is the imported file's id — so a
// declaration resolves by a direct lookup; an absent target is simply omitted,
// exactly as the project layer drops an unresolvable use. The result keys on
// file id (string), the form AnalyzeProgram and Program both bridge to FileID.
func Uses(docs map[string]*abstract.Document) map[string]map[*ast.UseDecl]string {
	out := make(map[string]map[*ast.UseDecl]string, len(docs))
	for id, doc := range docs {
		table := map[*ast.UseDecl]string{}
		for _, u := range doc.File().Uses {
			if _, ok := docs[u.Path]; ok {
				table[u] = u.Path
			}
		}
		out[id] = table
	}
	return out
}
