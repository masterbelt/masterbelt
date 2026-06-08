package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// This file is the allocation hard gate: allocs/op is a nearly
// deterministic metric — the same code path makes the same allocations — so a
// ceiling on it catches an allocation regression (a doubling when early cutoff
// collapses or a new per-node alloc creeps in) without the flakiness of a
// wall-clock fail condition. The ceilings carry headroom over the measured
// baseline: they are tripwires for a real regression, not exact matches, and
// the small run-to-run variance stays well under them. Re-baseline only when a
// change moves the count on purpose.
//
// Each gate uses testing.AllocsPerRun over a FIXED, small run count — not
// testing.Benchmark, whose default 1s benchtime would run hundreds of full
// compiles per invocation (and the gates run twice in CI: once in the plain
// `go test ./...`, once in `make perf`). A handful of runs is all an allocs/op
// reading needs. The gate also pins a fixed generator project, independent of
// the benchmark corpus's tuning, so re-sizing the benches never shifts it.

// gateProject is the fixed workload both ceilings measure: a 30-file synthetic
// project with a real use-graph, large enough that a cutoff collapse shows as
// a clear allocation blowup.
var gateProject = beltgen.Params{Files: 30, DeclsPerFile: 8, Depth: 3, Branching: 2, Seed: 7}

// allocGateRuns is the fixed sample count for the ceilings — enough for a
// stable allocs/op average, cheap enough to run twice in CI without cost.
const allocGateRuns = 8

const (
	// coldAllocCeiling bounds a full AnalyzeProgram of the gate project
	// (measured ~28.7k allocs/op via AllocsPerRun, ceiling at ~15% headroom).
	coldAllocCeiling = 33000
	// incrementalAllocCeiling bounds one keystroke through the incremental
	// path (measured ~1.6k allocs/op via AllocsPerRun over a warm replayer,
	// ceiling at ~30% headroom). A cutoff collapse — one edit recomputing the
	// whole project — would push this toward the cold count and trip the gate
	// long before the headroom matters.
	incrementalAllocCeiling = 2100
)

// TestColdCompileAllocCeiling fails when a from-scratch analysis of the gate
// project allocates past the ceiling — the one-shot CLI path's regression
// tripwire.
func TestColdCompileAllocCeiling(t *testing.T) {
	docs, uses := gateInputs()
	got := testing.AllocsPerRun(allocGateRuns, func() {
		AnalyzeProgram(docs, uses)
	})
	if int(got) > coldAllocCeiling {
		t.Errorf("cold compile allocs/op = %d, ceiling %d — an allocation regression (re-baseline only if intended)", int(got), coldAllocCeiling)
	}
}

// TestIncrementalAllocCeiling fails when one keystroke through the incremental
// Document.Edit + Refresh path allocates past the ceiling — the LSP hot path's
// regression tripwire, and the sharpest early-cutoff guard: a collapse makes
// the per-edit count balloon toward the cold count. The replayer is built once
// (warm); AllocsPerRun then measures only the per-edit work, each call
// advancing the insertion offset so the edits stay distinct.
func TestIncrementalAllocCeiling(t *testing.T) {
	entry := FileID(beltgen.EntryFile)
	r := newReplayer(genSources(gateProject))
	off := r.length(entry)
	got := testing.AllocsPerRun(allocGateRuns, func() {
		r.apply(entry, insertChar(off, '\n'))
		off++
	})
	if int(got) > incrementalAllocCeiling {
		t.Errorf("incremental edit allocs/op = %d, ceiling %d — early cutoff regressed or a new per-edit alloc crept in", int(got), incrementalAllocCeiling)
	}
}

// gateInputs builds the AnalyzeProgram inputs for the gate project, bridging
// beltgen's string ids to FileID through the shared UsesOf helper.
func gateInputs() (map[FileID]*abstract.Document, map[FileID]map[*ast.UseDecl]FileID) {
	raw := beltgen.Documents(beltgen.Project(gateProject))
	docs := make(map[FileID]*abstract.Document, len(raw))
	for id, d := range raw {
		docs[FileID(id)] = d
	}
	uses := make(map[FileID]map[*ast.UseDecl]FileID, len(raw))
	for id, u := range beltgen.Uses(raw) {
		uses[FileID(id)] = UsesOf(u)
	}
	return docs, uses
}
