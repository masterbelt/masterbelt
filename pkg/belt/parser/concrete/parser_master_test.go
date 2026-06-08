// This file tests parsing and recovery of master declarations.
package concrete

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// firstDeclKind returns the kind of the first node child of a parsed File — the
// first declaration — skipping the trivia and EOF leaves that are tokens.
func firstDeclKind(root *cst.Node) (cst.Kind, bool) {
	for _, c := range root.Children() {
		if n, ok := c.(*cst.Node); ok {
			return n.Kind(), true
		}
	}
	return 0, false
}

// TestParseMaster pins that a well-formed master parses to a MasterDecl with no
// diagnostics, and that a master still being typed — its name not yet written —
// recovers as a MasterDecl that reports the missing name, rather than degrading
// to a stray Error run (or, after pub, a misread constant). Keeping the
// MasterDecl shape under construction is what lets the editor hold an outline
// and symbols while the user writes the declaration.
func TestParseMaster(t *testing.T) {
	t.Run("well-formed", func(t *testing.T) {
		root, diags := Parse([]byte("master M {\n  record { id: int }\n  primary id\n}\n"))
		if len(diags) != 0 {
			t.Fatalf("want no diagnostics, got %v", diags)
		}
		if k, ok := firstDeclKind(root); !ok || k != cst.MasterDecl {
			t.Fatalf("first declaration = %v, want MasterDecl", k)
		}
	})

	for _, tc := range []struct{ name, src string }{
		{"missing name", "master {\n}\n"},
		{"missing name after pub", "pub master {\n}\n"},
		// A primary with no key, and a composite key with no column, both report
		// the missing identifier rather than recovering silently.
		{"primary without a key", "master M {\n  record { id: int }\n  primary\n}\n"},
		{"empty composite key", "master M {\n  record { id: int }\n  primary ()\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if k, ok := firstDeclKind(root); !ok || k != cst.MasterDecl {
				t.Fatalf("first declaration = %v, want MasterDecl", k)
			}
			found := false
			for _, d := range diags {
				if d.Code == CodeExpectedIdentifier {
					found = true
				}
			}
			if !found {
				t.Fatalf("src %q: want %s, got %v", tc.src, CodeExpectedIdentifier, diags)
			}
			assertLossless(t, tc.src)
		})
	}
}
