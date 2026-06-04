package abstract

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

var update = flag.Bool("update", false, "update the example snapshots in testdata/examples")

const (
	sharedExamples = "../../testdata/examples"
	snapshotDir    = "testdata/examples"
)

// TestExamples lowers every shared example and compares the AST dump against
// this package's committed snapshot. Refresh snapshots with:
//
//	go test ./pkg/masterbelt/parser/abstract/ -update
func TestExamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(sharedExamples, "*.belt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no example .belt files found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			file, _ := Lower(src)
			got := ast.Dump(file)

			snapshot := filepath.Join(snapshotDir, filepath.Base(path)+".ast")
			if *update {
				if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(snapshot, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatalf("missing snapshot (run: go test ./pkg/masterbelt/parser/abstract/ -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("snapshot mismatch for %s\n--- got ---\n%s--- want ---\n%s", filepath.Base(path), got, want)
			}
		})
	}
}
