package semantic

// This file is the performance equivalent of the IR snapshot: the reuse
// snapshot, the deterministic hard gate that is the plan's
// crown jewel. It generalizes the hand-written TestEarlyCutoff* assertions into
// a corpus-driven golden harness. For each (project source, edit) case it
// applies the edit through the incremental path and snapshots the per-kind
// recompute profile (Stats().Computed) to testdata/reuse/<name>.snap.
//
// A diff in one of these goldens means an edit's invalidation footprint
// changed: "this edit now recomputes module+typeOf+value where it used to
// recompute only value" shows up as a loud, reviewable change. Over-
// invalidation is a regression even when every behavior test still passes —
// the engine stays correct while quietly doing more work per keystroke, which
// is exactly the silent decay this gate exists to catch.
//
// The profile is counts only — no wall-clock, no nondeterministic value — so it
// is a deterministic metric and a hard gate. The engine is
// deterministic (pointer keys, revisioned memos), so a fixed edit on a fixed
// project recomputes exactly the same key set every run; if these counts ever
// vary across runs, the engine or this rendering is nondeterministic and must
// be investigated, not papered over.
//
// KNOWN OVER-INVALIDATION (do not mistake the goldens for the target): several
// cases — leaf_value, root_value_cascade, add_reference — currently recompute
// the whole file's const layer with zero reuse, because qSymbols is a coarse
// per-file pointer-keyed fact: reparsing any const decl gives it a fresh
// *ast.ConstDecl, equalSymbolValues sees the table changed, and every
// name-resolving query in the file recomputes. The goldens pin that footprint
// as a number so it is visible and reviewable, NOT because 17/0 is good. The
// day qSymbols is narrowed, these goldens SHRINK — that is an improvement to
// bless via -update, not a regression. A golden alone only catches a change,
// so it cannot catch a uniform collapse (every footprint widening together
// would just re-bless green); the minReused floor below is the absolute guard
// against that — it fails in code, independent of the goldens, if a case that
// must reuse stops reusing.
//
// Refresh the goldens with:
//
//	go test ./pkg/belt/semantic/ -run TestReuseSnapshot -update

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source"
)

const reuseSnapshotDir = "testdata/reuse"

// reuseCase is one (project source, edit) pair whose post-edit recompute
// profile is pinned to a golden. find/repl locate the edit by text so the case
// stays readable and survives source tweaks: repl replaces the first occurrence
// of find, and an insertion is find=="" anchored at the end of anchor.
//
// keepsLen records whether the edit is length-preserving. This is not cosmetic:
// the incremental document reparses by green-CST-node identity, so a length-
// changing edit shifts downstream byte offsets and hands those declarations
// fresh AST pointers, which defeats early cutoff by pointer identity and widens
// the recompute footprint. Cases that mean to demonstrate cutoff are length-
// preserving and assert it (so a later tweak can't silently break the invariant
// and quietly inflate the golden); cases that document a real cascade or a
// structural change are not, and say so.
type reuseCase struct {
	name     string
	src      string
	anchor   string // text immediately before an insertion point (find == "")
	find     string // text to replace (within the whole src); "" inserts at anchor's end
	repl     string // replacement text
	keepsLen bool   // the edit preserves source length (and so downstream offsets)
	// minReused is an absolute floor on TotalReused for cases where early
	// cutoff genuinely should reuse work (a body/consumer/type edit that leaves
	// the chain's facts intact). It is asserted in code, not via the golden, so
	// a uniform cutoff collapse — which would re-bless every golden green —
	// still fails here. Zero leaves the floor unasserted (the known
	// over-invalidation cases, which currently reuse nothing).
	minReused int
}

// editFor turns a case's text-anchored edit into a byte-offset source.Edit
// against its source, failing the test if the anchors cannot be located or the
// declared length invariant does not hold.
func (tc reuseCase) editFor(t *testing.T) source.Edit {
	t.Helper()
	if tc.find != "" {
		if tc.keepsLen && len(tc.find) != len(tc.repl) {
			t.Fatalf("setup %s: declared length-preserving but %q->%q changes length", tc.name, tc.find, tc.repl)
		}
		i := strings.Index(tc.src, tc.find)
		if i < 0 {
			t.Fatalf("setup %s: find %q not in src", tc.name, tc.find)
		}
		return source.Edit{Start: i, End: i + len(tc.find), NewText: []byte(tc.repl)}
	}
	if tc.keepsLen && tc.repl != "" {
		t.Fatalf("setup %s: an insertion cannot be length-preserving", tc.name)
	}
	a := strings.Index(tc.src, tc.anchor)
	if a < 0 {
		t.Fatalf("setup %s: anchor %q not in src", tc.name, tc.anchor)
	}
	at := a + len(tc.anchor)
	return source.Edit{Start: at, End: at, NewText: []byte(tc.repl)}
}

// reuseCases is the corpus: a shared multi-decl shape (a value-dependency chain
// const A -> B -> C, a type with a method, a top-level fn, and an assert
// consuming the chain) edited in ways that exercise both good cutoff (a leaf or
// consumer edit recomputes little) and a legitimate cascade (a widely-
// referenced root edit recomputes much). Each case's golden documents its
// invalidation footprint.
// chain is the canonical reuse fixture. The "  // pad" tail on the chain lines is
// slack the length-preserving cases edit into: it lets a value or body grow or
// shrink while the line — and every downstream offset — keeps its width.
const chain = "const A: long = 1   // pad\n" +
	"const B = A\n" +
	"const C = B         // pad\n" +
	"pub type T = { n: nint } impl {\n" +
	"  pub get size(): nint {\n" +
	"    return self.n       // pad\n" +
	"  }\n" +
	"}\n" +
	"pub fn twice(x: nint): nint {\n" +
	"  return x + x      // pad\n" +
	"}\n" +
	"assert C > 0\n"

func reuseCases() []reuseCase {

	// masterChain is the chain fixture with a master appended: a master is a
	// TypeDef, so editing its row method re-resolves the type-defs
	// layer, but the unrelated value chain (A->B->C, the assert) keeps its
	// memoized types and values — the cutoff the master shares with every other
	// type. The "// pad" tail is slack the length-preserving edit grows into so
	// downstream offsets stay put.
	// staticChain is the chain fixture with a static-fn requirement appended: an
	// interface requiring a static fn, a type meeting it, and a generic function
	// calling it through the bound. Editing the implementor's static fn body
	// re-checks that body, but the interface's requirement, the conformance, the
	// generic function's resolved call, and the unrelated value chain keep their
	// memoized facts. The "// pad" tail is slack the length-preserving edit grows
	// into so downstream offsets stay put.
	const staticChain = chain +
		"pub interface HasDefault {\n" +
		"  static defaultValue(): nint\n" +
		"}\n" +
		"pub type Counter = { c: nint } impl HasDefault {\n" +
		"  pub static fn defaultValue(): nint {\n" +
		"    return 0       // pad\n" +
		"  }\n" +
		"}\n" +
		"pub fn startValue<T: HasDefault>(): nint {\n" +
		"  return T.defaultValue()\n" +
		"}\n"

	const masterChain = chain +
		"pub master Skill {\n" +
		"  record {\n" +
		"    id: int,\n" +
		"    name: string,\n" +
		"  } impl {\n" +
		"    pub get label(): string {\n" +
		"      return self.name       // pad\n" +
		"    }\n" +
		"  }\n" +
		"  primary id\n" +
		"}\n"

	return []reuseCase{
		// Leaf-value edit (length-preserving): C is read by nothing, so changing
		// its initializer to an equal-width, same-typed expression keeps every
		// downstream offset put and is the smallest footprint cutoff allows.
		{name: "leaf_value", src: chain, keepsLen: true,
			find: "const C = B         ", repl: "const C = B + 0 - 0 "},
		// Root-value edit that legitimately cascades (length-preserving): A feeds
		// B feeds C feeds the assert, so re-evaluating A flows the whole chain —
		// and because the edit keeps offsets, the cascade is the engine's, not
		// the reparser's.
		{name: "root_value_cascade", src: chain, keepsLen: true,
			find: "const A: long = 1  ", repl: "const A: long = 5  "},
		// Annotation-only edit on the root (length-preserving): A's value is
		// unchanged, so the value side is cut off while the type side re-solves
		// the chain. long->nint keeps the width.
		{name: "root_annotation", src: chain, keepsLen: true,
			find: "const A: long", repl: "const A: nint"},
		// Assert-body edit (length-preserving): a pure consumer. The assert
		// re-checks (its diagnostic flips) but the constants it reads keep their
		// memoized type and value.
		{name: "assert_body", src: chain, keepsLen: true, minReused: 2,
			find: "assert C > 0", repl: "assert C > 9"},
		// Whitespace edit inside the fn body (length-preserving): a token-
		// identical line, shifting one space across the operator, so the analyzer
		// sees the same tokens and the footprint is near the floor.
		{name: "whitespace", src: chain, keepsLen: true, minReused: 2,
			find: "return x + x ", repl: "return x +  x"},
		// Comment-only edit (insertion): a new trailing comment line below the
		// chain. It changes no token the analyzer reads, but it shifts every
		// following decl's offset — so this case documents what a length-changing
		// no-op edit costs, the reparse-window floor.
		{name: "comment", src: chain, anchor: "const C = B         // pad\n", repl: "// added\n"},
		// Adding a reference (length-preserving): rewrite the leaf C to read A
		// directly. C now depends on A, but nothing depends on C, so the new
		// edge stays local.
		{name: "add_reference", src: chain, keepsLen: true,
			find: "const C = B", repl: "const C = A"},
		// Type-field rename (length-preserving): n -> m in both the field and the
		// getter body. The type changes shape, exercising the type-defs /
		// signature path that the value chain does not.
		{name: "type_field_rename", src: chain, keepsLen: true, minReused: 5,
			find: "{ n: nint }", repl: "{ m: nint }"},
		// Method-body edit that does not change the getter's result type (length-
		// preserving): self.n and self.n + 0 both type as nint, so dependents'
		// type side is cut off and only this method re-checks. The pad slack
		// absorbs the four extra characters.
		{name: "method_body", src: chain, keepsLen: true, minReused: 5,
			find: "return self.n       ", repl: "return self.n + 0   "},
		// Fn result-type edit (length-preserving): widening twice's result is a
		// contract change a caller would re-resolve; here it documents twice's
		// own re-typing footprint. nint->long keeps the width.
		{name: "fn_result_type", src: chain, keepsLen: true, minReused: 2,
			find: "(x: nint): nint", repl: "(x: nint): long"},
		// Master row-method edit (length-preserving): a master is a TypeDef, so
		// editing its row method re-resolves the type-defs layer, but the unrelated
		// value chain keeps its memoized types and values. self.name and
		// self.name + "" both type as string, so no dependent re-types. The pad
		// slack absorbs the edit's extra characters.
		{name: "master_method_body", src: masterChain, keepsLen: true, minReused: 5,
			find: "return self.name       ", repl: "return self.name + \"\"  "},
		// Static-fn body edit (length-preserving): the implementor's static fn
		// re-checks, but the interface's static requirement, the conformance, the
		// generic function calling T.defaultValue() through the bound, and the value
		// chain keep their memoized facts. return 0 and return 0 + 0 both type as
		// nint, so no dependent re-types; the pad slack absorbs the extra characters.
		{name: "static_fn_body", src: staticChain, keepsLen: true, minReused: 5,
			find: "return 0       ", repl: "return 0 + 0   "},
	}
}

// TestReuseSnapshot is the reuse-snapshot hard gate: for each corpus case it
// edits through the incremental path and pins the recomputed per-kind profile
// to a golden. See the file header for the contract and the -update workflow.
func TestReuseSnapshot(t *testing.T) {
	for _, tc := range reuseCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newEditable([]byte(tc.src))
			e.edit(tc.editFor(t))
			stats := e.prog.Stats()
			if stats.TotalReused < tc.minReused {
				t.Errorf("%s reused %d queries, floor %d — early cutoff regressed for an edit that should reuse work", tc.name, stats.TotalReused, tc.minReused)
			}
			compareReuseSnapshot(t, tc.name, renderProfile(stats))
		})
	}
}

// compareReuseSnapshot compares (or, under -update, writes) the rendered
// profile against testdata/reuse/<name>.snap. It mirrors compareSnapshot but
// targets the reuse golden dir.
func compareReuseSnapshot(t *testing.T, name, got string) {
	t.Helper()
	snapshot := filepath.Join(reuseSnapshotDir, name+".snap")
	if *update {
		if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snapshot, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("missing snapshot (run: go test ./pkg/belt/semantic/ -run TestReuseSnapshot -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("reuse-profile mismatch for %s\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

// renderProfile formats a revision's recompute profile deterministically: one
// "kind count" line per recomputed kind, sorted by kind, then a totals line.
// Counts only — no wall-clock — so the golden is a deterministic hard gate.
func renderProfile(s Stats) string {
	kinds := make([]string, 0, len(s.Computed))
	for kind := range s.Computed {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	var b strings.Builder
	for _, kind := range kinds {
		fmt.Fprintf(&b, "%s %d\n", kind, s.Computed[kind])
	}
	fmt.Fprintf(&b, "TotalComputed %d\nTotalReused %d\n", s.TotalComputed, s.TotalReused)
	return b.String()
}
