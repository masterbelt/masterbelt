package semantic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// anchorProgram is the diagnostic-free fixture the anchor query tests share: a
// file id "game.belt" (module segment "game") declaring one of every anchored
// kind — a nominal type with a method and an associated constant, an enum, an
// interface with a member, a top-level constant, an overloaded function, and a
// plain function.
const anchorSource = `/// A character level.
pub type Level = sbyte impl {
  /// The highest level.
  pub const Max = 100

  /// The next level.
  pub fn next(): self {
    return self + 1
  }
}

/// A card rarity.
pub enum Rarity: byte {
  Common = 1
  Rare = 2
}

/// A foldable behaviour.
pub interface Foldable {
  /// The required fold.
  fold(): nint
}

/// The starting level.
const Start: Level = 7

/// Scales an nint.
pub fn scale(x: nint): nint -> x * 10
/// Scales a list of nints.
pub fn scale(xs: list<nint>): list<nint> -> xs.map(fn(x) -> x * 10)

/// The area of a rectangle.
pub fn area(w: nint, h: nint): nint -> w * h
`

// TestAnchorStrings pins the pure address grammar: the module segment is the
// file path without .belt (empty for the sole file, "builtin" for the prelude),
// a declaration anchor is belt:module/name, a member hangs off it with #, and an
// unnamed declaration or member has no address at all.
func TestAnchorStrings(t *testing.T) {
	if got := moduleSegment("game.belt"); got != "game" {
		t.Errorf("moduleSegment(game.belt) = %q, want game", got)
	}
	if got := moduleSegment(soleFileID); got != "" {
		t.Errorf("moduleSegment(sole) = %q, want empty", got)
	}
	if got := moduleSegment("builtin.belt"); got != "builtin" {
		t.Errorf("moduleSegment(prelude) = %q, want builtin", got)
	}
	if got := declAnchor("game", "Level"); got != "belt:game/Level" {
		t.Errorf("declAnchor = %q", got)
	}
	if got := declAnchor("", "Top"); got != "belt:/Top" {
		t.Errorf("declAnchor(sole) = %q, want belt:/Top", got)
	}
	if got := declAnchor("game", ""); got != "" {
		t.Errorf("declAnchor of unnamed = %q, want empty", got)
	}
	if got := memberAnchor("belt:game/Level", "next"); got != "belt:game/Level#next" {
		t.Errorf("memberAnchor = %q", got)
	}
	if got := memberAnchor("", "next"); got != "" {
		t.Errorf("memberAnchor of unnamed owner = %q, want empty", got)
	}
	if got := memberAnchor("belt:game/Level", ""); got != "" {
		t.Errorf("memberAnchor of unnamed member = %q, want empty", got)
	}
}

// anchorDesc tags a resolved node with its kind and name, so a ByAnchor result
// is asserted as a compact string ("type Level", "func scale") regardless of
// which concrete IR struct it is.
func anchorDesc(n any) string {
	switch d := n.(type) {
	case *ir.Const:
		return "const " + d.Name
	case *ir.TypeDef:
		return "type " + d.Name
	case *ir.Method:
		return "method " + d.Name
	case *ir.AssocConst:
		return "assoc " + d.Name
	case *ir.Function:
		return "func " + d.Name
	default:
		return fmt.Sprintf("?%T", n)
	}
}

func anchorDescs(nodes []any) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, anchorDesc(n))
	}
	return out
}

// TestProgramByAnchor resolves one anchor of every kind back to its declaration
// through the whole program.
func TestProgramByAnchor(t *testing.T) {
	p := buildProgram(map[string]string{"game.belt": anchorSource})
	assertClean(t, p, "game.belt")

	cases := []struct {
		anchor string
		want   string
	}{
		{"belt:game/Level", "type Level"},
		{"belt:game/Level#next", "method next"},
		{"belt:game/Level#Max", "assoc Max"},
		{"belt:game/Rarity", "type Rarity"},
		{"belt:game/Foldable", "type Foldable"},
		{"belt:game/Foldable#fold", "method fold"},
		{"belt:game/Start", "const Start"},
		{"belt:game/area", "func area"},
	}
	for _, c := range cases {
		got := anchorDescs(p.ByAnchor(c.anchor))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("ByAnchor(%q) = %v, want [%q]", c.anchor, got, c.want)
		}
	}

	// A bare overloaded name names the whole set.
	overload := p.ByAnchor("belt:game/scale")
	if len(overload) != 2 {
		t.Fatalf("ByAnchor(scale) = %v, want two functions", anchorDescs(overload))
	}

	// A signature suffix narrows it to the one individual — spaced or not.
	for _, anchor := range []string{"belt:game/scale(nint)", "belt:game/scale( nint )"} {
		got := p.ByAnchor(anchor)
		if len(got) != 1 {
			t.Fatalf("ByAnchor(%q) = %v, want one function", anchor, anchorDescs(got))
		}
		fn, ok := got[0].(*ir.Function)
		if !ok || len(fn.Params) != 1 || fn.Params[0].Type.String() != "nint" {
			t.Errorf("ByAnchor(%q) selected the wrong overload: %v", anchor, anchorDescs(got))
		}
	}
	if got := p.ByAnchor("belt:game/scale(list<nint>)"); len(got) != 1 {
		t.Errorf("ByAnchor(scale(list<nint>)) = %v, want one function", anchorDescs(got))
	}

	// The empty address and an address nothing carries resolve to nothing.
	if got := p.ByAnchor(""); got != nil {
		t.Errorf(`ByAnchor("") = %v, want nil`, anchorDescs(got))
	}
	if got := p.ByAnchor("belt:game/Missing"); got != nil {
		t.Errorf("ByAnchor(Missing) = %v, want nil", anchorDescs(got))
	}
}

// TestEnclosingDecl maps a byte offset to the address of the smallest
// declaration spanning it — a method body resolves to the method, not the
// enclosing type — and an offset in no declaration resolves to nothing.
func TestEnclosingDecl(t *testing.T) {
	p := buildProgram(map[string]string{"game.belt": anchorSource})
	assertClean(t, p, "game.belt")

	cases := []struct {
		needle string
		want   string
	}{
		{"self + 1", "belt:game/Level#next"}, // a method body resolves to the method
		{"= 7", "belt:game/Start"},           // a constant initializer to the constant
		{"w * h", "belt:game/area"},          // a function body to the function
	}
	for _, c := range cases {
		offset := strings.Index(anchorSource, c.needle)
		if offset < 0 {
			t.Fatalf("needle %q not in fixture", c.needle)
		}
		got, ok := p.EnclosingDecl("game.belt", offset+1)
		if !ok || got != c.want {
			t.Errorf("EnclosingDecl(offset of %q) = %q (ok=%v), want %q", c.needle, got, ok, c.want)
		}
	}

	// One past the end is inside no declaration.
	if got, ok := p.EnclosingDecl("game.belt", len(anchorSource)); ok {
		t.Errorf("EnclosingDecl(eof) = %q (ok=%v), want no declaration", got, ok)
	}
	// An unknown file has no module, hence no enclosing declaration.
	if _, ok := p.EnclosingDecl("missing.belt", 0); ok {
		t.Error("EnclosingDecl of an unknown file reported ok=true")
	}
}

// TestAnchorIncremental pins the stability rule (A-5 §7): editing a
// declaration's body leaves its anchor unchanged, while renaming it changes the
// anchor (the address tracks the name, not the position).
func TestAnchorIncremental(t *testing.T) {
	p := buildProgram(map[string]string{"game.belt": "const Answer = 42\n"})
	if got := anchorDescs(p.ByAnchor("belt:game/Answer")); len(got) != 1 {
		t.Fatalf("initial ByAnchor(Answer) = %v, want the constant", got)
	}

	// A body edit (same name) keeps the address.
	setFile(p, "game.belt", "const Answer = 43\n")
	got := p.ByAnchor("belt:game/Answer")
	if len(got) != 1 {
		t.Fatalf("after body edit ByAnchor(Answer) = %v, want the constant", anchorDescs(got))
	}
	if c := got[0].(*ir.Const); c.Anchor != "belt:game/Answer" {
		t.Errorf("after body edit anchor = %q, want belt:game/Answer", c.Anchor)
	}

	// A rename moves the address: the old one resolves to nothing, the new one
	// to the declaration.
	setFile(p, "game.belt", "const Reply = 42\n")
	if got := p.ByAnchor("belt:game/Answer"); got != nil {
		t.Errorf("after rename ByAnchor(Answer) = %v, want nil", anchorDescs(got))
	}
	if got := anchorDescs(p.ByAnchor("belt:game/Reply")); len(got) != 1 || got[0] != "const Reply" {
		t.Errorf("after rename ByAnchor(Reply) = %v, want [const Reply]", got)
	}
}

// setFile re-installs one file's source and refreshes, the editing step the
// incremental anchor test drives.
func setFile(p *Program, id FileID, src string) {
	p.SetFile(id, abstract.NewDocument([]byte(src)), nil)
	p.Refresh()
}
