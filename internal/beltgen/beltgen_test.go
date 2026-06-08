package beltgen_test

import (
	"fmt"
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// corpus is the canonical set of generator sizes the tests and benches share:
// a single file, a shallow fan-out, a padded mid tree, and two deeper trees, so
// a curve over file count and graph depth is visible.
var corpus = []beltgen.Params{
	{Files: 1, DeclsPerFile: 4, Depth: 0, Branching: 0, Seed: 1},
	{Files: 3, DeclsPerFile: 6, Depth: 1, Branching: 2, Seed: 7},
	{Files: 20, DeclsPerFile: 8, Depth: 2, Branching: 3, Seed: 9},
	{Files: 50, DeclsPerFile: 10, Depth: 3, Branching: 2, Seed: 99},
	{Files: 100, DeclsPerFile: 12, Depth: 3, Branching: 3, Seed: 123},
}

// bridge converts a generated project to the FileID-keyed inputs the engine
// consumes: parsed documents and the per-file use table.
func bridge(srcs map[string][]byte) (map[semantic.FileID]*abstract.Document, map[semantic.FileID]map[*ast.UseDecl]semantic.FileID) {
	docs := beltgen.Documents(srcs)
	uses := beltgen.Uses(docs)

	sdocs := make(map[semantic.FileID]*abstract.Document, len(docs))
	for id, d := range docs {
		sdocs[semantic.FileID(id)] = d
	}
	suses := make(map[semantic.FileID]map[*ast.UseDecl]semantic.FileID, len(uses))
	for id, t := range uses {
		suses[semantic.FileID(id)] = semantic.UsesOf(t)
	}
	return sdocs, suses
}

// label renders a generator size as a stable sub-benchmark/sub-test name.
func label(p beltgen.Params) string {
	return fmt.Sprintf("files=%d/decls=%d/depth=%d/branch=%d", p.Files, p.DeclsPerFile, p.Depth, p.Branching)
}

// reportDiags fails the test with the first few diagnostics, so a generator
// regression is legible rather than just a count.
func reportDiags(t *testing.T, diags map[semantic.FileID][]diagnostic.Diagnostic) {
	t.Helper()
	for id, ds := range diags {
		for _, d := range ds {
			t.Errorf("%s: [%s] %s", id, d.Code, d.Message)
		}
	}
}

// TestBeltgenAnalyzesClean is the corpus's reason to exist: a synthetic project
// is only a useful benchmark if it type-checks, so every generated size must
// analyze with zero diagnostics. A regression here means the generator drifted
// from the language and the bench numbers would be measuring error reporting.
func TestBeltgenAnalyzesClean(t *testing.T) {
	for _, p := range corpus {
		t.Run(label(p), func(t *testing.T) {
			srcs := beltgen.Project(p)
			if len(srcs) < p.Files {
				t.Fatalf("generated %d files, want at least %d", len(srcs), p.Files)
			}
			sdocs, suses := bridge(srcs)
			_, diags := semantic.AnalyzeProgram(sdocs, suses)
			total := 0
			for _, ds := range diags {
				total += len(ds)
			}
			if total != 0 {
				reportDiags(t, diags)
			}
		})
	}
}

// TestBeltgenDeterministic pins the generator's determinism contract: the same
// Params must yield byte-equal sources, with no clock or unseeded randomness.
func TestBeltgenDeterministic(t *testing.T) {
	p := beltgen.Params{Files: 30, DeclsPerFile: 7, Depth: 2, Branching: 3, Seed: 555}
	a, b := beltgen.Project(p), beltgen.Project(p)
	if len(a) != len(b) {
		t.Fatalf("file counts differ: %d vs %d", len(a), len(b))
	}
	for id, src := range a {
		if string(b[id]) != string(src) {
			t.Fatalf("file %s differs between identical Params", id)
		}
	}
}

// TestBeltgenUseGraph checks the generated dependency graph really has the
// requested shape: the entry imports Branching children, and the tree reaches
// the requested depth.
func TestBeltgenUseGraph(t *testing.T) {
	p := beltgen.Params{Files: 13, DeclsPerFile: 5, Depth: 2, Branching: 3, Seed: 3}
	paths := beltgen.UsePaths(p)
	if got := len(paths[beltgen.EntryFile]); got != p.Branching {
		t.Fatalf("entry imports %d children, want %d", got, p.Branching)
	}
	if !reaches(paths, beltgen.EntryFile, p.Depth) {
		t.Fatalf("use-graph does not reach depth %d", p.Depth)
	}
}

// reaches reports whether the use-graph has a path of the given length from id.
func reaches(paths map[string][]string, id string, depth int) bool {
	if depth == 0 {
		return true
	}
	for _, child := range paths[id] {
		if reaches(paths, child, depth-1) {
			return true
		}
	}
	return false
}
