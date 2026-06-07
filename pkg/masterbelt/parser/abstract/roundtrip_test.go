// This file holds the AST half of the text-representation gates (F-4 §2.4):
// P1 canonicity (re-marshal of an unmarshal is byte-identical), P2 golden
// survival (every committed .ast snapshot parses back to its own bytes), and
// F1 (the unmarshaler never panics). The corpus is the shared example set —
// the same sources the snapshots are lowered from.
package abstract

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// TestTextRoundTrip pins P1 over the corpus: every lowered example marshals,
// unmarshals to a detached File, and re-marshals byte-identically.
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
			file, _ := Lower(src)
			first, err := file.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText: %v", err)
			}
			var back ast.File
			if err := back.UnmarshalText(first); err != nil {
				t.Fatalf("UnmarshalText: %v", err)
			}
			if back.Syntax() != nil {
				t.Error("unmarshaled File carries a syntax backpointer; detachment is the contract")
			}
			second, err := back.MarshalText()
			if err != nil {
				t.Fatalf("re-MarshalText: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Error("re-marshal is not byte-identical (P1)")
			}
		})
	}
}

// TestSnapshotsUnmarshal pins P2: every committed .ast snapshot unmarshals and
// re-marshals to its own bytes — the goldens are a living contract whose
// format rot fails CI, not just diff fodder.
func TestSnapshotsUnmarshal(t *testing.T) {
	matches, err := snapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no .ast snapshots found")
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
			var file ast.File
			if err := file.UnmarshalText(data); err != nil {
				t.Fatalf("snapshot does not unmarshal: %v", err)
			}
			again, err := file.MarshalText()
			if err != nil {
				t.Fatalf("re-MarshalText: %v", err)
			}
			if !bytes.Equal(again, data) {
				t.Error("snapshot is not canonical: unmarshal+marshal diverges from the committed bytes")
			}
		})
	}
}

// snapshotFiles lists every committed .ast snapshot, the flat examples and the
// project ones.
func snapshotFiles() ([]string, error) {
	flat, err := filepath.Glob(filepath.Join(snapshotDir, "*.ast"))
	if err != nil {
		return nil, err
	}
	nested, err := filepath.Glob(filepath.Join(snapshotDir, "*", "*.ast"))
	if err != nil {
		return nil, err
	}
	return append(flat, nested...), nil
}

// FuzzASTUnmarshal is the F1 gate: the unmarshaler accepts or rejects any
// input without panicking, and whatever it accepts marshals to a fixpoint of
// the round trip.
func FuzzASTUnmarshal(f *testing.F) {
	matches, err := snapshotFiles()
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte("File\n"))
	f.Add([]byte("File\n  Uses: ~\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var file ast.File
		if err := file.UnmarshalText(data); err != nil {
			return // rejected: fine, as long as it never panics
		}
		first, err := file.MarshalText()
		if err != nil {
			t.Fatalf("accepted input fails to marshal: %v", err)
		}
		var again ast.File
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
