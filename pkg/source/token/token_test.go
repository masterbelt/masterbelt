package token

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestKindString(t *testing.T) {
	if got := Const.String(); got != "Const" {
		t.Errorf("Const.String() = %q, want %q", got, "Const")
	}
	if got := Kind(999).String(); got != "Kind(999)" {
		t.Errorf("Kind(999).String() = %q, want %q", got, "Kind(999)")
	}
}

// TestKindNamesComplete binds the Kind const block to the hand-maintained
// kindNames table: every kind must have a non-empty name, and String() must
// never fall back to "Kind(N)" for a real kind. Without it, a Kind added to the
// enum but forgotten in kindNames renders silently as "Kind(63)" in token dumps
// and diagnostics. numKinds makes the bound automatic.
func TestKindNamesComplete(t *testing.T) {
	for k := Kind(0); k < numKinds; k++ {
		name := k.String()
		if name == "" {
			t.Errorf("Kind(%d) has an empty name", int(k))
		}
		if strings.HasPrefix(name, "Kind(") {
			t.Errorf("Kind(%d) has no kindNames entry; String() fell back to %q", int(k), name)
		}
	}
}

// TestKindNamesUnique guards against a copy-paste slip duplicating a name.
func TestKindNamesUnique(t *testing.T) {
	seen := map[string]Kind{}
	for k := Kind(0); k < numKinds; k++ {
		name := k.String()
		if prev, dup := seen[name]; dup {
			t.Errorf("Kind name %q is shared by Kind(%d) and Kind(%d)", name, int(prev), int(k))
		}
		seen[name] = k
	}
}

// TestKeywordKindsMapped binds the keyword Kind run to the keywords map: every
// kind from firstKeyword to lastKeyword must appear as a value there. A keyword
// Kind missing from the map is treated as a plain identifier by the lexer
// (silent wrong behavior) and is absent from the generated editor grammar, with
// nothing else failing. The reverse — every map entry naming a kind in the
// keyword run — guards against a stray non-keyword slipping in.
func TestKeywordKindsMapped(t *testing.T) {
	mapped := map[Kind]string{}
	for spelling, k := range keywords {
		if prev, dup := mapped[k]; dup {
			t.Errorf("keyword kind %s is mapped by both %q and %q", k, prev, spelling)
		}
		mapped[k] = spelling
		if k < firstKeyword || k > lastKeyword {
			t.Errorf("keywords[%q] = %s, which is outside the keyword Kind run [%s..%s]", spelling, k, firstKeyword, lastKeyword)
		}
	}
	for k := firstKeyword; k <= lastKeyword; k++ {
		spelling, ok := mapped[k]
		if !ok {
			t.Errorf("keyword kind %s has no entry in the keywords map; the lexer would treat its word as an identifier", k)
			continue
		}
		// The mapping must round-trip through Lookup, the lexer's actual path.
		if got := Lookup(spelling); got != k {
			t.Errorf("Lookup(%q) = %s, want %s", spelling, got, k)
		}
	}
}

// TestOperatorKindsHaveSpelling binds the operator/punctuation Kind run to the
// spelling map: every kind from firstOperator to lastOperator must have a
// non-empty spelling, since that map is the source of truth for naming operators
// in diagnostics and feeds the editor grammar. A new operator added without a
// spelling entry would render as "" in those places.
func TestOperatorKindsHaveSpelling(t *testing.T) {
	for k := firstOperator; k <= lastOperator; k++ {
		if s := k.Symbol(); s == "" {
			t.Errorf("operator kind %s has no spelling entry", k)
		}
	}
	// Nothing outside the operator run claims a spelling (which would mean a
	// keyword or literal kind wandered into the operator map).
	for k, s := range spelling {
		if k < firstOperator || k > lastOperator {
			t.Errorf("spelling[%s] = %q, outside the operator Kind run [%s..%s]", k, s, firstOperator, lastOperator)
		}
	}
}

// TestOperators pins Operators() to the operator Kind run: it must list every
// kind from firstOperator to lastOperator exactly once, in order, each with a
// non-empty spelling and a name that parses back to its kind. It is the source
// the editor grammars read, so a gap or a stray entry there is a lexer drift.
func TestOperators(t *testing.T) {
	ops := Operators()
	if got, want := len(ops), int(lastOperator-firstOperator+1); got != want {
		t.Fatalf("Operators() returned %d entries, want %d", got, want)
	}
	for i, op := range ops {
		want := firstOperator + Kind(i)
		if op.Name != want.String() {
			t.Errorf("Operators()[%d].Name = %q, want %q", i, op.Name, want.String())
		}
		if op.Symbol == "" {
			t.Errorf("Operators()[%d] (%s) has an empty spelling", i, op.Name)
		}
		if k, ok := ParseKind(op.Name); !ok || k != want {
			t.Errorf("ParseKind(%q) = %v, %v; want %v", op.Name, k, ok, want)
		}
	}
}

func TestLookup(t *testing.T) {
	cases := map[string]Kind{
		"const":   Const,
		"pub":     Pub,
		"type":    Type,
		"impl":    Impl,
		"fn":      Fn,
		"return":  Return,
		"self":    Self,
		"null":    Null,
		"extern":  Extern,
		"builtin": Builtin,
		"let":     Let,
		"long":    Ident, // a type name is an ordinary identifier, not a keyword
		"x":       Ident,
	}
	for ident, want := range cases {
		if got := Lookup(ident); got != want {
			t.Errorf("Lookup(%q) = %s, want %s", ident, got, want)
		}
	}
}

func TestTokenResolution(t *testing.T) {
	file := source.NewFile("t.belt", []byte("const x\n"))
	tok := Token{Kind: Const, Offset: 0, Width: 5}

	if got := tok.End(); got != 5 {
		t.Errorf("End() = %d, want 5", got)
	}
	if got := tok.Text(file); got != "const" {
		t.Errorf("Text() = %q, want %q", got, "const")
	}
	if got := tok.String(); got != "Const@0+5" {
		t.Errorf("String() = %q, want %q", got, "Const@0+5")
	}

	span := tok.Span(file)
	if span.Start.Column != 1 || span.End.Column != 6 {
		t.Errorf("Span() columns = (%d, %d), want (1, 6)", span.Start.Column, span.End.Column)
	}
	if got := span.Len(); got != tok.Width {
		t.Errorf("Span().Len() = %d, want %d", got, tok.Width)
	}
}

// TestParseKindRoundTrip pins the reverse registry to the String table: every
// real kind parses back from its name, and non-names are rejected.
func TestParseKindRoundTrip(t *testing.T) {
	for k := range numKinds {
		got, ok := ParseKind(k.String())
		if !ok || got != k {
			t.Errorf("ParseKind(%q) = %v, %v; want %v", k.String(), got, ok, k)
		}
	}
	for _, name := range []string{"", "Kind(3)", "ident", "Bogus"} {
		if _, ok := ParseKind(name); ok {
			t.Errorf("ParseKind(%q) accepted, want rejection", name)
		}
	}
}
