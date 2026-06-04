package semantic

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

var update = flag.Bool("update", false, "update the example snapshots in testdata/examples")

const (
	sharedExamples = "../testdata/examples"
	snapshotDir    = "testdata/examples"
)

// TestExamples analyzes every shared example and compares the IR dump against
// this package's committed snapshot. Refresh with:
//
//	go test ./pkg/masterbelt/semantic/ -update
func TestExamples(t *testing.T) {
	paths, err := exampleSources(sharedExamples)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no example .belt files found")
	}

	for _, path := range paths {
		name, err := filepath.Rel(sharedExamples, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			module, _ := Analyze(abstract.NewDocument(src))
			got := ir.Dump(module)

			snapshot := filepath.Join(snapshotDir, name+".ir")
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
		})
	}
}

// exampleSources lists every shared example .belt: the flat single-file
// examples plus the files of project examples, which are directories holding a
// masterbelt.toml and several .belt sources. For now each project file is
// analyzed standalone like any other example; the multi-file engine (P-2 M5)
// re-designs this layer's snapshot unit to the whole project.
func exampleSources(dir string) ([]string, error) {
	flat, err := filepath.Glob(filepath.Join(dir, "*.belt"))
	if err != nil {
		return nil, err
	}
	nested, err := filepath.Glob(filepath.Join(dir, "*", "*.belt"))
	if err != nil {
		return nil, err
	}
	return append(flat, nested...), nil
}
