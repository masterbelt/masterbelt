package semantic

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
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
//	go test ./pkg/masterbelt/semantic/ -update
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
				compareSnapshot(t, name+".ir", ir.Dump(module))
			})
		}
	}
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
		full := ir.Dump(modules[id])
		incremental := ir.Dump(prog.Module(id))
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
		t.Fatalf("missing snapshot (run: go test ./pkg/masterbelt/semantic/ -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("snapshot mismatch for %s\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}
