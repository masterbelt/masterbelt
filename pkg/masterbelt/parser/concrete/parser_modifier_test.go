package concrete

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
)

// implMethod is one MethodDecl node of an impl block together with its absolute
// byte offset within the file, so its modifier's text can be read from the
// source (green nodes carry only widths, never offsets).
type implMethod struct {
	node *cst.Node
	off  int
}

// implMethods returns the MethodDecl nodes of the first declaration's impl
// block, in source order, each with its absolute offset.
func implMethods(root *cst.Node) []implMethod {
	var out []implMethod
	// Walk the tree summing widths to recover absolute offsets.
	var off int
	decl := root.Children()[0].(*cst.Node)
	off = childOffsetWithin(root, decl)
	cur := off
	for _, c := range decl.Children() {
		impl, ok := c.(*cst.Node)
		if ok && impl.Kind() == cst.ImplBlock {
			icur := cur
			for _, ic := range impl.Children() {
				if m, ok := ic.(*cst.Node); ok && m.Kind() == cst.MethodDecl {
					out = append(out, implMethod{node: m, off: icur})
				}
				icur += ic.Width()
			}
		}
		cur += c.Width()
	}
	return out
}

// childOffsetWithin returns the absolute offset of child within parent, summing
// the widths of the children before it.
func childOffsetWithin(parent, child cst.Green) int {
	off := 0
	for _, c := range parent.(*cst.Node).Children() {
		if c == child {
			return off
		}
		off += c.Width()
	}
	return off
}

// modifierText returns the text of a method's leading Modifier node, or "" when
// the method carries no modifier. src is the whole source and m carries its
// absolute offset, so the Modifier's covered bytes are read directly.
func modifierText(src string, m implMethod) string {
	off := m.off
	for _, c := range m.node.Children() {
		mod, ok := c.(*cst.Node)
		if ok && mod.Kind() == cst.Modifier {
			return src[off : off+mod.Width()]
		}
		off += c.Width()
	}
	return ""
}

// TestParseMethodModifiers checks that the three accessor/static modifiers are
// recognized at a method's start and wrapped in a Modifier node, and that the
// static modifier consumes its fn keyword (so the method's name is the next
// identifier, not "static"/"fn").
func TestParseMethodModifiers(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // the recognized modifier, or "" for an ordinary method
	}{
		{"getter", "type C = { d: nint } impl {\n  pub get fahrenheit(): nint {\n    return self.d\n  }\n}\n", "get"},
		{"setter", "type C = { d: nint } impl {\n  pub set fahrenheit(v: nint): self {\n    return self\n  }\n}\n", "set"},
		{"static", "type C = { d: nint } impl {\n  pub static fn make(): C {\n    return self\n  }\n}\n", "static"},
		{"static with effect", "type C = { d: nint } impl {\n  static fn io now(): C {\n    return self\n  }\n}\n", "static"},
		{"ordinary method", "type C = { d: nint } impl {\n  pub deg(): nint {\n    return self.d\n  }\n}\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			ms := implMethods(root)
			if len(ms) != 1 {
				t.Fatalf("methods = %d, want 1", len(ms))
			}
			if got := modifierText(tc.src, ms[0]); got != tc.want {
				t.Fatalf("modifier = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseGetSetAsMethodNames checks the context-keyword discrimination: get
// and set are modifiers only when an identifier follows them on the same line;
// the prelude's get(index) / set(k, v) — and a method literally named get or
// set — stay ordinary methods, since a "(" follows rather than an identifier.
func TestParseGetSetAsMethodNames(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"get method", "type C = { d: nint } impl {\n  pub get(i: nint): nint {\n    return self.d\n  }\n}\n", ""},
		{"set method", "type C = { d: nint } impl {\n  pub set(k: nint, v: nint): self {\n    return self\n  }\n}\n", ""},
		{"static method named static", "type C = { d: nint } impl {\n  pub static(): nint {\n    return self.d\n  }\n}\n", ""},
		{"getter named get", "type C = { d: nint } impl {\n  pub get get(): nint {\n    return self.d\n  }\n}\n", "get"},
		{"setter named set", "type C = { d: nint } impl {\n  pub set set(v: nint): self {\n    return self\n  }\n}\n", "set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, diags := Parse([]byte(tc.src))
			if len(diags) != 0 {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			ms := implMethods(root)
			if len(ms) != 1 {
				t.Fatalf("methods = %d, want 1", len(ms))
			}
			if got := modifierText(tc.src, ms[0]); got != tc.want {
				t.Fatalf("modifier = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseStaticMissingFn checks the recovery for a forgotten fn: `static name`
// reads static as the modifier and reports expected_fn, rather than misreading
// static as the method name and cascading errors over the real signature.
func TestParseStaticMissingFn(t *testing.T) {
	src := "type C = { d: nint } impl {\n  pub static make(): C {\n    return self\n  }\n}\n"
	root, diags := Parse([]byte(src))
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one expected_fn", diags)
	}
	if diags[0].Code != CodeExpectedFn {
		t.Fatalf("diagnostic code = %s, want %s", diags[0].Code, CodeExpectedFn)
	}
	ms := implMethods(root)
	if len(ms) != 1 {
		t.Fatalf("methods = %d, want 1", len(ms))
	}
	if got := modifierText(src, ms[0]); got != "static" {
		t.Fatalf("modifier = %q, want %q", got, "static")
	}
}

// TestParseModifierNotAcrossNewline checks the lookahead stops at a newline: a
// get/set at a line's end is the (mis-spelled) name of its own member, not a
// modifier reaching onto the next line.
func TestParseModifierNotAcrossNewline(t *testing.T) {
	// `get` then a newline then a record body: get is the method name (the
	// parser does not pull the next line's identifier into a modifier).
	src := "type C = { d: nint } impl {\n  pub get\n  bad(): nint {\n    return self.d\n  }\n}\n"
	root, _ := Parse([]byte(src))
	ms := implMethods(root)
	if len(ms) == 0 {
		t.Fatalf("no methods parsed")
	}
	if got := modifierText(src, ms[0]); got != "" {
		t.Fatalf("modifier = %q across a newline, want none", got)
	}
}

// TestParseExternGetIsError checks that `extern get name` is not a valid accessor
// — the grammar has no extern accessor, so the modifier is not recognized after
// extern and the stray tokens are reported.
func TestParseExternGetIsError(t *testing.T) {
	src := "type C = { d: nint } impl {\n  pub extern get fahrenheit(): nint\n}\n"
	_, diags := Parse([]byte(src))
	if len(diags) == 0 {
		t.Fatalf("expected a diagnostic for `extern get`, got none")
	}
}

// TestModifierKindString pins the Modifier kind's name, since the snapshot and
// the AST lowering read it.
func TestModifierKindString(t *testing.T) {
	if got := cst.Modifier.String(); got != "Modifier" {
		t.Fatalf("Modifier.String() = %q, want Modifier", got)
	}
}

// TestParseAccessorRoundTrip checks losslessness over the new modifier node: the
// in-order leaf walk reproduces the source byte for byte.
func TestParseAccessorRoundTrip(t *testing.T) {
	src := "type C = { d: nint } impl {\n  pub get hot(): nint {\n    return self.d\n  }\n  pub static fn make(): C {\n    return self\n  }\n}\n"
	root, _ := Parse([]byte(src))
	var b strings.Builder
	var walk func(g cst.Green, off int)
	walk = func(g cst.Green, off int) {
		switch n := g.(type) {
		case *cst.Node:
			cur := off
			for _, c := range n.Children() {
				walk(c, cur)
				cur += c.Width()
			}
		case *cst.Token:
			b.WriteString(src[off : off+n.Width()])
		}
	}
	walk(root, 0)
	if b.String() != src {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", b.String(), src)
	}
}
