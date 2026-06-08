package semantic

import (
	"testing"
)

// TestMapKeyBoundAnnotation pins the K: comparable bound on the map declaration
// at an annotation site: a comparable key type (string, an enum) is accepted,
// while an anonymous record key — which is not comparable — is reported as
// bound_not_satisfied.
func TestMapKeyBoundAnnotation(t *testing.T) {
	t.Run("string key ok", func(t *testing.T) {
		_, diags := analyze("const ok: map<string, nint> = [\"a\": 1]\n")
		if len(diags) != 0 {
			t.Fatalf("string is comparable; want clean, got %v", codes(diags))
		}
	})

	t.Run("record key not comparable", func(t *testing.T) {
		_, diags := analyze("const bad: map<{x: nint}, nint> = []\n")
		if !hasCode(diags, CodeBoundNotSatisfied) {
			t.Fatalf("anonymous record is not comparable; want bound_not_satisfied, got %v", codes(diags))
		}
	})

	t.Run("enum key ok", func(t *testing.T) {
		src := "pub enum Rarity {\n  Common\n  Rare\n}\n" +
			"const enumKey: map<Rarity, nint> = []\n"
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Fatalf("enum is automatically comparable; want clean, got %v", codes(diags))
		}
	})

	t.Run("list key ok", func(t *testing.T) {
		// list is comparable (it impls comparable in the prelude), so a list key
		// is accepted — the composite-key path the evaluator already supports.
		_, diags := analyze("const ok: map<list<nint>, nint> = []\n")
		if len(diags) != 0 {
			t.Fatalf("list is comparable; want clean, got %v", codes(diags))
		}
	})
}

// TestMapKeyBoundInferred checks the inferred map-literal path: a literal whose
// key type is a concrete non-comparable record is reported even without a
// written annotation, the key-contract violation surfacing at the literal. The
// key is a nominal record (Point) — an anonymous record literal has no inferable
// type of its own, so its own uninferable diagnostic stands; a nominal one does
// infer, and its non-comparable type is what the inferred-path check catches.
func TestMapKeyBoundInferred(t *testing.T) {
	src := "pub type Point = { x: nint, y: nint }\n" +
		"const bad = [Point{ x: 1, y: 2 }: \"v\"]\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeBoundNotSatisfied) {
		t.Fatalf("inferred record key is not comparable; want bound_not_satisfied, got %v", codes(diags))
	}
}
