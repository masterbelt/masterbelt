package concrete

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

var update = flag.Bool("update", false, "update the example snapshots in testdata/examples")

// formatSnapshot renders a parsed example as a stable, diffable snapshot: the
// concrete tree (one element per line, indented), then the parse diagnostics
// with their resolved line:col. It is the parser's counterpart to the lexer's
// token snapshot.
func formatSnapshot(buf source.Buffer, root cst.Green, diags []diagnostic.Diagnostic) string {
	var b strings.Builder
	b.WriteString("# tree\n")
	b.WriteString(cst.Sprint(buf, root))
	b.WriteString("# diagnostics\n")
	for _, d := range diags {
		s := d.Span(buf).Start
		fmt.Fprintf(&b, "%s[%s] %d:%d %s\n", d.Severity, d.Code, s.Line, s.Column, d.Message)
	}
	return b.String()
}

// sharedExamples holds the .belt sample sources shared by every masterbelt
// package; snapshotDir holds this package's own expected tree snapshots.
const (
	sharedExamples = "../../testdata/examples"
	snapshotDir    = "testdata/examples"
)

// TestExamples parses every shared example and compares the result against this
// package's committed snapshot. Refresh snapshots with:
//
//	go test ./pkg/masterbelt/parser/concrete/ -update
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

			file := source.NewFile(path, src)
			root, diags := Parse(src)
			got := formatSnapshot(file, root, diags)

			snapshot := filepath.Join(snapshotDir, name+".cst")
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
				t.Fatalf("missing snapshot (run: go test ./pkg/masterbelt/parser/concrete/ -update): %v", err)
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
