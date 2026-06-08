package semantic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// benchCorpus is the generator-size sweep the incremental benchmarks and the
// replay corpus test share, mirroring internal/beltgen's corpus but expressed
// over the engine's FileID inputs.
var benchCorpus = []beltgen.Params{
	{Files: 3, DeclsPerFile: 6, Depth: 1, Branching: 2, Seed: 7},
	{Files: 20, DeclsPerFile: 8, Depth: 2, Branching: 3, Seed: 9},
	{Files: 50, DeclsPerFile: 10, Depth: 3, Branching: 2, Seed: 99},
}

// genSources renders a generator size into the FileID-keyed sources the
// replayer consumes.
func genSources(p beltgen.Params) map[FileID][]byte {
	raw := beltgen.Project(p)
	out := make(map[FileID][]byte, len(raw))
	for id, src := range raw {
		out[FileID(id)] = src
	}
	return out
}

// corpusLabel names a generator size for a sub-test/sub-benchmark.
func corpusLabel(p beltgen.Params) string {
	return fmt.Sprintf("files=%d/decls=%d/depth=%d/branch=%d", p.Files, p.DeclsPerFile, p.Depth, p.Branching)
}

// entryEdit is the realistic keystroke the corpus replay and the incremental
// benchmark both apply: a one-character insert at the very end of the entry
// file, the editor gesture the whole loop is built to measure. End-of-file keeps
// the offset valid for any generated entry.
func entryEdit(src []byte) source.Edit {
	return insertChar(len(src), '\n')
}

// TestEditReplayCorpus drives the replay harness over every generator size: a
// single keystroke on the entry file must leave the program clean and must
// reuse work rather than recompute everything — an edit that recomputed every
// query would mean the incremental path collapsed to a cold compile, the exact
// regression D-1's loop exists to catch.
func TestEditReplayCorpus(t *testing.T) {
	for _, p := range benchCorpus {
		t.Run(corpusLabel(p), func(t *testing.T) {
			srcs := genSources(p)
			entry := FileID(beltgen.EntryFile)
			r := newReplayer(srcs)
			assertReplayClean(t, r, entry)

			stats := r.apply(entry, entryEdit(srcs[entry]))
			assertReplayClean(t, r, entry)

			if stats.TotalReused == 0 {
				t.Fatalf("a single keystroke reused no queries; the incremental path collapsed to a cold compile")
			}
		})
	}
}

// TestEditReplaySoleScripts exercises the realistic single-file gestures
// (keystroke, paste, value edit) through the replay harness on a small
// hand-written file, so the harness's edit constructors are proven to keep a
// well-formed program clean.
func TestEditReplaySoleScripts(t *testing.T) {
	const src = "pub const Base = 1\npub const Derived = Base + 2\n"
	for _, sc := range soleScripts(src) {
		t.Run(sc.name, func(t *testing.T) {
			r := newReplayer(map[FileID][]byte{soleFileID: []byte(src)})
			for _, ed := range sc.steps {
				r.apply(sc.file, ed)
			}
			assertReplayClean(t, r, soleFileID)
		})
	}
}

// TestEditReplayRename renames a declaration and its sole reader in one session,
// proving the rename gesture keeps a program consistent: renaming only the
// declaration would orphan the reader, so the harness renames both occurrences
// and the program must stay clean.
func TestEditReplayRename(t *testing.T) {
	const src = "pub const Base = 1\npub const Derived = Base + 2\n"
	r := newReplayer(map[FileID][]byte{soleFileID: []byte(src)})

	// Rename the declaration "Base" -> "Root" (same length, so the reader's
	// offset is stable), then the reader's reference.
	declOff := strings.Index(src, "Base")
	r.apply(soleFileID, rename(declOff, "Base", "Root"))
	refOff := strings.LastIndex(src, "Base")
	r.apply(soleFileID, rename(refOff, "Base", "Root"))

	assertReplayClean(t, r, soleFileID)
}

// TestEditReplayAddUse adds a cross-file import through the harness: a second
// file exports a constant, and a use prepended to the first file plus a
// reference to the imported name must analyze clean — the import gesture
// re-wires the dependency graph the replayer resolves.
func TestEditReplayAddUse(t *testing.T) {
	srcs := map[FileID][]byte{
		"main.belt": []byte("pub const Local = 1\n"),
		"lib.belt":  []byte("pub const Shared = 41\n"),
	}
	r := newReplayer(srcs)
	assertReplayClean(t, r, "main.belt")

	// Prepend `use lib from "lib.belt"`, then append a reference that reads the
	// imported constant through the new namespace.
	r.apply("main.belt", addUse("lib", "lib.belt"))
	r.apply("main.belt", pasteBlock(r.length("main.belt"), "pub const Sum = Local + lib.Shared\n"))

	assertReplayClean(t, r, "main.belt")
}
