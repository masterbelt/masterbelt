package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// replayer drives a multi-file Program the way an editor session does: an edit
// goes to one file's syntax document, that file is re-pushed with its
// re-resolved use table, and the program is refreshed — the LSP-realistic
// incremental path (D-1 §3 edit-replay). It is the multi-file generalization of
// edit_test.go's editable, used by the incremental benchmarks and reusable by
// later trend work.
//
// It keeps the documents and a flat path->FileID use resolution (the
// program_test buildProgram stand-in for the project layer), so a replayed edit
// that adds a `use` re-wires the graph exactly as the project layer's Resync
// would on disk.
type replayer struct {
	prog *Program
	docs map[FileID]*abstract.Document
}

// newReplayer builds a Program over srcs (path -> source) and refreshes it once,
// returning the editable session. A use whose path names no file in srcs stays
// unresolved, as on disk.
func newReplayer(srcs map[FileID][]byte) *replayer {
	docs := make(map[FileID]*abstract.Document, len(srcs))
	for id, src := range srcs {
		docs[id] = abstract.NewDocument(src)
	}
	r := &replayer{prog: NewProgram(), docs: docs}
	for id := range docs {
		r.push(id)
	}
	r.prog.Refresh()
	return r
}

// length is the current byte length of a file's buffer — the valid offset for
// an end-of-file insertion after earlier edits.
func (r *replayer) length(file FileID) int {
	return r.docs[file].Buffer().Len()
}

// usesOfDoc re-resolves one document's use declarations against the session's
// flat file set: a use path is the imported file's id, and an unresolvable path
// is omitted — the same rule the project layer applies.
func (r *replayer) usesOfDoc(doc *abstract.Document) map[*ast.UseDecl]FileID {
	uses := map[*ast.UseDecl]FileID{}
	for _, u := range doc.File().Uses {
		if _, ok := r.docs[FileID(u.Path)]; ok {
			uses[u] = FileID(u.Path)
		}
	}
	return uses
}

// push installs one file with its freshly resolved use table, without
// refreshing — the caller refreshes once per edit.
func (r *replayer) push(id FileID) {
	r.prog.SetFile(id, r.docs[id], r.usesOfDoc(r.docs[id]))
}

// apply performs one edit on file: the text change goes through the incremental
// Document.Edit path (so the engine's cutoff engages on shared AST pointers,
// never the all-new pointers a fresh parse would make), the file is re-pushed,
// and the program is refreshed. It returns the work the refresh did, so a
// caller can pin or report the reuse.
func (r *replayer) apply(file FileID, ed source.Edit) Stats {
	r.docs[file].Edit(ed)
	r.push(file)
	r.prog.Refresh()
	return r.prog.Stats()
}

// The realistic edit kinds of D-1 §3, each a constructor for a source.Edit
// given a position in the current text. They are deliberately tiny so a replay
// sequence reads as the editor gestures it models.

// insertChar is a single-character insertion at off — the keystroke that
// dominates an editor session.
func insertChar(off int, c byte) source.Edit {
	return source.Edit{Start: off, End: off, NewText: []byte{c}}
}

// pasteBlock inserts a multi-line block at off — a paste, the burst edit that
// most stresses re-parse and re-analysis.
func pasteBlock(off int, block string) source.Edit {
	return source.Edit{Start: off, End: off, NewText: []byte(block)}
}

// rename replaces the identifier occurrence [off, off+len(old)) with repl — the
// rename gesture, which the engine must ripple to every reader of the renamed
// declaration.
func rename(off int, old, repl string) source.Edit {
	return source.Edit{Start: off, End: off + len(old), NewText: []byte(repl)}
}

// addUse prepends a use declaration at the top of a file — the import gesture,
// which re-wires the dependency graph and forces a re-resolution of the file's
// imports.
func addUse(ns, path string) source.Edit {
	return source.Edit{Start: 0, End: 0, NewText: []byte("use " + ns + " from \"" + path + "\"\n")}
}

// editScript is a named sequence of edits against one file, the unit a replay
// benchmark or trend run iterates. Each step's offset is into the text as it
// stands when the step runs, so a script reads in order.
type editScript struct {
	name  string
	file  FileID
	steps []source.Edit
}

// soleScripts returns the realistic single-file edit gestures of D-1 §3 against
// a one-file program whose source is src: a keystroke, a paste, a rename, and a
// new use. Each script is independent — a benchmark resets the session between
// scripts — so the offsets are all into the original src.
func soleScripts(src string) []editScript {
	end := len(src)
	dot := strings.Index(src, " = ") + len(" = ")
	return []editScript{
		{"keystroke", soleFileID, []source.Edit{insertChar(end, '\n')}},
		{"paste", soleFileID, []source.Edit{pasteBlock(end, "const Pasted = 1 + 2 + 3\n")}},
		{"editValue", soleFileID, []source.Edit{insertChar(dot, '1')}},
	}
}

// assertReplayClean is the replay oracle for tests: after a script the
// incrementally analyzed file must carry no diagnostics it did not start with —
// the realistic edits the harness emits are all well-formed, so a replay that
// dirties the program means the harness, not the engine, drifted.
func assertReplayClean(t *testing.T, r *replayer, file FileID) {
	t.Helper()
	if diags := r.prog.Diagnostics(file); len(diags) != 0 {
		t.Fatalf("%s diagnostics after replay = %v, want none", file, diags)
	}
}
