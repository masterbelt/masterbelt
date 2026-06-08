package semantic

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

var update = flag.Bool("update", false, "update the example snapshots in testdata/examples")

const (
	sharedExamples = "../testdata/examples"
	snapshotDir    = "testdata/examples"
)

// TestExamples analyzes every shared example and compares the IR dump against
// this package's committed snapshot. A flat <name>.belt analyzes standalone; a
// project directory (masterbelt.toml + several .belt) analyzes as one program
// — through both the reference AnalyzeProgram and the incremental Program,
// which must agree — and snapshots as one <name>.ir with a section per file.
// Refresh with:
//
//	go test ./pkg/belt/semantic/ -update
func TestExamples(t *testing.T) {
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no examples found")
	}

	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			t.Run(name, func(t *testing.T) {
				compareSnapshot(t, name+".ir", dumpProject(t, filepath.Join(sharedExamples, name)))
			})
		case strings.HasSuffix(name, ".belt"):
			t.Run(name, func(t *testing.T) {
				src, err := os.ReadFile(filepath.Join(sharedExamples, name))
				if err != nil {
					t.Fatal(err)
				}
				module, _ := Analyze(abstract.NewDocument(src))
				compareSnapshot(t, name+".ir", dumpIR(t, module))
			})
		}
	}
}

// dumpIR renders a module in the exact text form — the snapshot oracle and
// the incremental-equality oracle alike.
func dumpIR(t *testing.T, m *ir.Module) string {
	t.Helper()
	text, err := m.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	return string(text)
}

// dumpProject opens the example project, analyzes it twice — the from-scratch
// oracle and the incremental engine — checks the two agree, and renders the
// per-file modules in id order.
func dumpProject(t *testing.T, dir string) string {
	t.Helper()
	proj, pdiags := project.Open(dir)
	if pdiags.Len() > 0 {
		t.Fatalf("project diagnostics: %v", pdiags.Items())
	}

	docs := map[FileID]*abstract.Document{}
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	prog := NewProgram()
	for _, f := range proj.Files() {
		id := FileID(f.ID)
		fileUses := UsesOf(f.Uses)
		docs[id] = f.AST
		uses[id] = fileUses
		prog.SetFile(id, f.AST, fileUses)
	}
	prog.Refresh()
	modules, _ := AnalyzeProgram(docs, uses)

	var b strings.Builder
	for _, id := range prog.Files() {
		full := dumpIR(t, modules[id])
		incremental := dumpIR(t, prog.Module(id))
		if incremental != full {
			t.Errorf("%s: incremental != full analysis\n--- incremental ---\n%s--- full ---\n%s", id, incremental, full)
		}
		fmt.Fprintf(&b, "# %s\n%s", id, full)
	}
	return b.String()
}

func compareSnapshot(t *testing.T, name, got string) {
	t.Helper()
	snapshot := filepath.Join(snapshotDir, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snapshot, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("missing snapshot (run: go test ./pkg/belt/semantic/ -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("snapshot mismatch for %s\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

// TestExamplesAnalyzeClean pins that every shared example is diagnostic-free
// across all layers — lexing, parsing, and semantic analysis. The .ir
// snapshots alone cannot catch a type error (evaluation folds by value kind
// regardless of typing), so a broken example would otherwise stay green in
// tests while showing an error in the editor — exactly how a Level-plus-int8
// operand mistake in 0030 once slipped through.
func TestExamplesAnalyzeClean(t *testing.T) {
	entries, err := os.ReadDir(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir():
			t.Run(name, func(t *testing.T) { analyzeProjectExample(t, name) })
		case strings.HasSuffix(name, ".belt"):
			t.Run(name, func(t *testing.T) { analyzeFileExample(t, name) })
		}
	}
}

// reportExampleLayers reports any lex or parse diagnostics a document carries,
// each prefixed with the given label.
func reportExampleLayers(t *testing.T, label string, doc *abstract.Document) {
	t.Helper()
	for _, d := range doc.Concrete().LexDiagnostics() {
		t.Errorf("%s: lex diagnostic: %s @%d: %s", label, d.Code, d.Offset, d.Message)
	}
	for _, d := range doc.Diagnostics() {
		t.Errorf("%s: parse diagnostic: %s @%d: %s", label, d.Code, d.Offset, d.Message)
	}
}

// analyzeProjectExample opens a multi-file example project, reports its lex and
// parse diagnostics, and asserts whole-program analysis is diagnostic-free.
func analyzeProjectExample(t *testing.T, name string) {
	t.Helper()
	proj, pdiags := project.Open(filepath.Join(sharedExamples, name))
	if pdiags.Len() > 0 {
		t.Fatalf("project diagnostics: %v", pdiags.Items())
	}
	docs := map[FileID]*abstract.Document{}
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	for _, f := range proj.Files() {
		reportExampleLayers(t, string(f.ID), f.AST)
		docs[FileID(f.ID)] = f.AST
		uses[FileID(f.ID)] = UsesOf(f.Uses)
	}
	_, diags := AnalyzeProgram(docs, uses)
	for id, ds := range diags {
		for _, d := range ds {
			t.Errorf("%s: semantic diagnostic: %s @%d: %s", id, d.Code, d.Offset, d.Message)
		}
	}
}

// analyzeFileExample reads a single-file example, reports its lex and parse
// diagnostics, and asserts analysis is diagnostic-free.
func analyzeFileExample(t *testing.T, name string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(sharedExamples, name))
	if err != nil {
		t.Fatal(err)
	}
	doc := abstract.NewDocument(src)
	reportExampleLayers(t, name, doc)
	_, diags := Analyze(doc)
	for _, d := range diags {
		t.Errorf("%s: semantic diagnostic: %s @%d: %s", name, d.Code, d.Offset, d.Message)
	}
}
