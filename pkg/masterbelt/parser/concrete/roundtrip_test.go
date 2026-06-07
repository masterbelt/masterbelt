// This file holds the CST half of the text-representation gates (F-4 §2.4):
// P1 canonicity (re-marshal of an unmarshal is byte-identical), P2 golden
// survival (every committed .cst snapshot parses back), P3 losslessness (the
// unmarshaled tree still reproduces the source byte for byte), and F1 (the
// unmarshaler never panics on arbitrary input). The corpus is the shared
// example set — the same sources the snapshots are built from.
package concrete

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// treeSection extracts the tree text from a snapshot file: the lines between
// the "# tree" header and the "# diagnostics" trailer.
func treeSection(t *testing.T, snapshot []byte) []byte {
	t.Helper()
	s := string(snapshot)
	body, ok := strings.CutPrefix(s, "# tree\n")
	if !ok {
		t.Fatal("snapshot does not start with # tree")
	}
	tree, _, ok := strings.Cut(body, "# diagnostics\n")
	if !ok {
		t.Fatal("snapshot has no # diagnostics trailer")
	}
	return []byte(tree)
}

// TestTextRoundTrip parses every shared example and pins P1 and P3 on it: the
// marshaled tree unmarshals to an equal tree whose re-marshal is byte-equal
// (canonicity), and the unmarshaled — fully detached — tree still reproduces
// the source exactly (losslessness without a buffer).
func TestTextRoundTrip(t *testing.T) {
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
			root, _ := Parse(src)
			first, err := root.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText: %v", err)
			}
			var back cst.Node
			if err := back.UnmarshalText(first); err != nil {
				t.Fatalf("UnmarshalText: %v", err)
			}
			if !cst.Equal(root, &back) {
				t.Error("unmarshaled tree differs from the parsed one")
			}
			second, err := back.MarshalText()
			if err != nil {
				t.Fatalf("re-MarshalText: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Error("re-marshal is not byte-identical (P1)")
			}
			if got := cst.Source(&back); !bytes.Equal(got, src) {
				t.Error("detached tree does not reproduce the source (P3)")
			}
		})
	}
}

// TestSnapshotsUnmarshal pins P2: every committed .cst snapshot's tree section
// is parseable — the goldens are a living contract, not just diff fodder.
func TestSnapshotsUnmarshal(t *testing.T) {
	matches, err := snapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no .cst snapshots found")
	}
	for _, path := range matches {
		name, err := filepath.Rel(snapshotDir, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var n cst.Node
			if err := n.UnmarshalText(treeSection(t, data)); err != nil {
				t.Errorf("snapshot does not unmarshal: %v", err)
			}
		})
	}
}

// snapshotFiles lists every committed .cst snapshot, the flat examples and the
// project ones.
func snapshotFiles() ([]string, error) {
	flat, err := filepath.Glob(filepath.Join(snapshotDir, "*.cst"))
	if err != nil {
		return nil, err
	}
	nested, err := filepath.Glob(filepath.Join(snapshotDir, "*", "*.cst"))
	if err != nil {
		return nil, err
	}
	return append(flat, nested...), nil
}

// FuzzCSTUnmarshal is the F1 gate: the unmarshaler accepts or rejects any
// input without panicking, and whatever it accepts is canonical — its marshal
// is a fixpoint of the round trip.
func FuzzCSTUnmarshal(f *testing.F) {
	matches, err := snapshotFiles()
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		s := string(data)
		if body, ok := strings.CutPrefix(s, "# tree\n"); ok {
			if tree, _, ok := strings.Cut(body, "# diagnostics\n"); ok {
				f.Add([]byte(tree))
			}
		}
	}
	f.Add([]byte("File\n"))
	f.Add([]byte("File\n  Ident \"x\"\n"))
	f.Add([]byte("  Indented\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var n cst.Node
		if err := n.UnmarshalText(data); err != nil {
			return // rejected: fine, as long as it never panics
		}
		first, err := n.MarshalText()
		if err != nil {
			t.Fatalf("accepted input fails to marshal: %v", err)
		}
		var again cst.Node
		if err := again.UnmarshalText(first); err != nil {
			t.Fatalf("canonical form fails to unmarshal: %v", err)
		}
		second, err := again.MarshalText()
		if err != nil {
			t.Fatalf("canonical form fails to re-marshal: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("marshal is not a fixpoint:\n--- first ---\n%s--- second ---\n%s", first, second)
		}
	})
}
