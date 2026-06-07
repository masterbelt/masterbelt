package cst

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// TestParseKindRoundTrip pins the reverse registry to the String table: every
// real kind parses back from its name, and non-names are rejected.
func TestParseKindRoundTrip(t *testing.T) {
	for k := range numKinds {
		got, ok := ParseKind(k.String())
		if !ok || got != k {
			t.Errorf("ParseKind(%q) = %v, %v; want %v", k.String(), got, ok, k)
		}
	}
	for _, name := range []string{"", "Kind(3)", "file", "Bogus"} {
		if _, ok := ParseKind(name); ok {
			t.Errorf("ParseKind(%q) accepted, want rejection", name)
		}
	}
}

// TestMarshalElementForms pins the two element forms: a token marshals to its
// kind and quoted text, a node to its kind heading its indented children.
func TestMarshalElementForms(t *testing.T) {
	tok := NewToken(token.Ident, "x")
	if got, _ := tok.MarshalText(); string(got) != "Ident \"x\"\n" {
		t.Errorf("token marshal = %q", got)
	}
	node := NewNode(NameRef, []Green{tok})
	if got, _ := node.MarshalText(); string(got) != "NameRef\n  Ident \"x\"\n" {
		t.Errorf("node marshal = %q", got)
	}
}

// TestUnmarshalRejects pins the unmarshaler's error paths: each malformed
// input is rejected with an error, never accepted or panicked on.
func TestUnmarshalRejects(t *testing.T) {
	for name, input := range map[string]string{
		"empty":              "",
		"token root":         "Ident \"x\"\n",
		"unknown node kind":  "Bogus\n",
		"unknown token kind": "File\n  Bogus \"x\"\n",
		"malformed quote":    "File\n  Ident \"x\n",
		"unquoted tail":      "File\n  Ident x\n",
		"second root":        "File\nFile\n",
		"depth jump":         "File\n    Ident \"x\"\n",
		"indented root":      "  File\n",
	} {
		var n Node
		if err := n.UnmarshalText([]byte(input)); err == nil {
			t.Errorf("%s: UnmarshalText(%q) accepted, want error", name, input)
		}
	}
}

// TestUnmarshalDetachedEquality pins that an unmarshaled tree is Equal to the
// one it was marshaled from and reproduces its source.
func TestUnmarshalDetachedEquality(t *testing.T) {
	root := NewNode(File, []Green{
		NewNode(ConstDecl, []Green{
			NewToken(token.Const, "const"),
			NewToken(token.Whitespace, " "),
			NewToken(token.Ident, "X"),
		}),
		NewToken(token.Newline, "\n"),
	})
	data, err := root.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Node
	if err := back.UnmarshalText(data); err != nil {
		t.Fatal(err)
	}
	if !Equal(root, &back) {
		t.Error("round-tripped tree is not Equal to the original")
	}
	if got, want := string(Source(&back)), "const X\n"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
}

// TestSprintIsMarshal pins Sprint as the string form of MarshalText.
func TestSprintIsMarshal(t *testing.T) {
	root := NewNode(File, []Green{NewToken(token.Ident, "x")})
	data, _ := root.MarshalText()
	if Sprint(root) != string(data) {
		t.Error("Sprint diverges from MarshalText")
	}
	if !strings.HasPrefix(Sprint(root), "File\n") {
		t.Errorf("Sprint = %q", Sprint(root))
	}
}
