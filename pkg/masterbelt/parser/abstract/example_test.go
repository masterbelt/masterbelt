package abstract

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
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

			file, _ := Lower(src)
			text, err := file.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText: %v", err)
			}
			got := string(text)

			snapshot := filepath.Join(snapshotDir, name+".ast")
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
				t.Fatalf("missing snapshot (run: go test ./pkg/masterbelt/parser/abstract/ -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("snapshot mismatch for %s\n--- got ---\n%s--- want ---\n%s", name, got, want)
			}
		})
	}
}

// exampleSources lists every shared example .belt: the flat single-file
// examples plus the files of project examples, which are directories holding a
// masterbelt.toml and several .belt sources. Each project file goes through
// this layer like any other example — the project as a whole only matters to
// the layers that resolve across files.
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
