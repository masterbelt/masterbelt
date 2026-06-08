package semantic

import (
	"maps"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

// TestStatsDoesNotPerturbCutoff is the M1 invariant (D-1 §8-2): reading the
// performance side-channel must not change what the engine memoizes. The same
// incremental edit is replayed on two programs — one whose Stats() is read on
// every Refresh, one never read — and their recompute sets must be identical.
// A stat that leaked into a memo value or equalValue would make the observed
// program recompute differently, which this catches.
func TestStatsDoesNotPerturbCutoff(t *testing.T) {
	src := []byte("const A = 1\nconst B = A\nconst C = B\n")
	edit := source.Edit{Start: 10, End: 11, NewText: []byte("2")} // A = 1 -> A = 2

	observed := newEditable(src)
	_ = observed.prog.Stats()
	observed.edit(edit)
	got := observed.prog.Stats() // read on the observed program

	silent := newEditable(src)
	silent.edit(edit) // never read
	want := silent.prog.Stats()

	// The two programs parse independently, so their queryKey pointers differ;
	// the invariant is that the per-kind recompute and reuse counts match —
	// instrumentation changed no decision the cutoff made.
	if !maps.Equal(got.Computed, want.Computed) {
		t.Errorf("reading Stats changed the recompute profile:\n observed %v\n silent   %v", got.Computed, want.Computed)
	}
	if !maps.Equal(got.Reused, want.Reused) {
		t.Errorf("reading Stats changed the reuse profile:\n observed %v\n silent   %v", got.Reused, want.Reused)
	}
}

// TestStatsReuseReflectsCutoff pins that the counters report what early cutoff
// actually did: editing a leaf constant no one depends on recomputes its own
// queries while the rest survive as reused. The exact numbers are the
// reuse-snapshot's business (M3); here we pin the shape — a steady edit reuses
// more than nothing, proving the counter observes the cutoff.
func TestStatsReuseReflectsCutoff(t *testing.T) {
	// const C = B is the leaf; rewriting its initializer to a literal recomputes
	// C's value/type but leaves A and B untouched.
	e := newEditable([]byte("const A = 1\nconst B = A\nconst C = B\n"))
	e.edit(source.Edit{Start: 34, End: 35, NewText: []byte("9")}) // "C = B" -> "C = 9"

	s := e.prog.Stats()
	if s.TotalComputed == 0 {
		t.Fatal("editing C recomputed nothing; expected at least C's own queries")
	}
	if s.TotalReused == 0 {
		t.Error("editing only C reused nothing; A's and B's queries should have survived the cutoff")
	}
}

// TestKindNamesComplete pins that every query kind has a stable stats label —
// a kind added to the enum without a kindNames entry would render as its
// number in every snapshot and stats output, which this rejects.
func TestKindNamesComplete(t *testing.T) {
	for k := qInput; k <= qModule; k++ {
		if _, ok := kindNames[k]; !ok {
			t.Errorf("query kind %d (%q) has no kindNames label", int(k), k.String())
		}
	}
}
