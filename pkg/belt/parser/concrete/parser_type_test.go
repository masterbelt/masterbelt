// This file tests the parsing of type expressions — record and qualified type
// names and their recovery — mirroring parser_type.go.
package concrete

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// TestParseRecordTypeSeparators checks that a record type accepts comma
// separators alongside newlines, matching the literal's separator rule.
func TestParseRecordTypeSeparators(t *testing.T) {
	cases := []string{
		"type Point = { x: nint, y: nint }\n",
		"type Point = { x: nint, y: nint, }\n",
		"type Point = {\n  x: nint\n  y: nint\n}\n",
	}
	for _, src := range cases {
		root, diags := Parse([]byte(src))
		if len(diags) != 0 {
			t.Fatalf("%q: unexpected diagnostics: %v", src, diags)
		}
		assertLossless(t, src)
		decl := root.Children()[0].(*cst.Node)
		got := subNodeKinds(decl)
		if len(got) != 1 || got[0] != cst.RecordType {
			t.Fatalf("%q: sub-nodes = %v, want [RecordType]", src, got)
		}
	}
}

// findQualifiedTypeName returns the first TypeName node carrying a Dot child —
// the qualified form geo.Point — anywhere under g.
func findQualifiedTypeName(g cst.Green) *cst.Node {
	n, ok := g.(*cst.Node)
	if !ok {
		return nil
	}
	if n.Kind() == cst.TypeName {
		for _, c := range n.Children() {
			if tok, ok := c.(*cst.Token); ok && tok.Kind() == token.Dot {
				return n
			}
		}
	}
	for _, c := range n.Children() {
		if hit := findQualifiedTypeName(c); hit != nil {
			return hit
		}
	}
	return nil
}

// TestParseQualifiedTypeName: a namespace-qualified type name (geo.Point)
// parses into a single TypeName holding qualifier, dot, and name, in every
// type position.
func TestParseQualifiedTypeName(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"annotation", "const a: geo.Point = 1\n"},
		{"generic argument", "const a: list<geo.Point> = [1]\n"},
		{"union member", "type P = geo.Point | null\n"},
		{"record field", "type R = {\n  p: geo.Point\n}\n"},
		{"func type", "type F = fn(p: geo.Point): geo.Point\n"},
		{"applied", "const a: geo.Box<nint> = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			name := findQualifiedTypeName(root)
			if name == nil {
				t.Fatal("no qualified TypeName in the tree")
			}
			var kinds []token.Kind
			for _, c := range name.Children() {
				if tok, ok := c.(*cst.Token); ok {
					kinds = append(kinds, tok.Kind())
				}
			}
			want := []token.Kind{token.Ident, token.Dot, token.Ident}
			if len(kinds) != len(want) {
				t.Fatalf("token children = %v, want %v", kinds, want)
			}
			for i := range want {
				if kinds[i] != want[i] {
					t.Fatalf("token children = %v, want %v", kinds, want)
				}
			}
		})
	}
}

// TestParseQualifiedTypeNameRecovery: a dangling qualifier (geo.) reports
// expected_identifier, and the declaration still closes over its initializer.
func TestParseQualifiedTypeNameRecovery(t *testing.T) {
	root, diags := Parse([]byte("const a: geo. = 1\n"))
	if len(diags) != 1 || diags[0].Code != CodeExpectedIdentifier {
		t.Fatalf("diagnostics = %v, want exactly expected_identifier", diags)
	}
	decl := root.Children()[0].(*cst.Node)
	if decl.Kind() != cst.ConstDecl {
		t.Fatalf("first child = %s, want ConstDecl", decl.Kind())
	}
	got := subNodeKinds(decl)
	want := []cst.Kind{cst.TypeClause, cst.Initializer}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sub-nodes = %v, want %v (recovery must keep the initializer)", got, want)
	}
}
