package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// This file is the allocation hard gate (D-1 §4.1): allocs/op is a nearly
// deterministic metric — the same code path makes the same allocations — so a
// ceiling on it catches an allocation regression (a doubling when early cutoff
// collapses or a new per-node alloc creeps in) without the flakiness of a
// wall-clock fail condition. The ceilings carry headroom over the measured
// baseline: they are tripwires for a real regression, not exact matches, and
// the ~2% run-to-run variance from benchmark iteration counts stays well under
// them. Re-baseline only when a change moves the count on purpose.
//
// The gate pins a FIXED generator project, independent of the benchmark
// corpus's tuning, so re-sizing the benches never silently shifts the gate.

// gateProject is the fixed workload both ceilings measure: a 30-file synthetic
// project with a real use-graph, large enough that a cutoff collapse shows as
// a clear allocation blowup.
var gateProject = beltgen.Params{Files: 30, DeclsPerFile: 8, Depth: 3, Branching: 2, Seed: 7}

const (
	// coldAllocCeiling bounds a full AnalyzeProgram of the gate project
	// (measured ~28.7k allocs/op, ceiling at ~25% headroom).
	coldAllocCeiling = 36000
	// incrementalAllocCeiling bounds one keystroke through the incremental
	// path (measured ~3.5k allocs/op, ceiling at ~30% headroom). A cutoff
	// collapse — one edit recomputing the whole project — would push this
	// toward the cold count and trip the gate.
	incrementalAllocCeiling = 4600
)

// TestColdCompileAllocCeiling fails when a from-scratch analysis of the gate
// project allocates past the ceiling — the one-shot CLI path's regression
// tripwire.
func TestColdCompileAllocCeiling(t *testing.T) {
	docs, uses := gateInputs()
	res := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for range b.N {
			AnalyzeProgram(docs, uses)
		}
	})
	if got := res.AllocsPerOp(); got > coldAllocCeiling {
		t.Errorf("cold compile allocs/op = %d, ceiling %d — an allocation regression (re-baseline only if intended)", got, coldAllocCeiling)
	}
}

// TestIncrementalAllocCeiling fails when one keystroke through the incremental
// Document.Edit + Refresh path allocates past the ceiling — the LSP hot path's
// regression tripwire, and the sharpest early-cutoff guard: a collapse makes
// the per-edit count balloon toward the cold count.
func TestIncrementalAllocCeiling(t *testing.T) {
	entry := FileID(beltgen.EntryFile)
	res := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		r := newReplayer(genSources(gateProject))
		off := r.length(entry)
		for range b.N {
			r.apply(entry, insertChar(off, '\n'))
			off++
		}
	})
	if got := res.AllocsPerOp(); got > incrementalAllocCeiling {
		t.Errorf("incremental edit allocs/op = %d, ceiling %d — early cutoff regressed or a new per-edit alloc crept in", got, incrementalAllocCeiling)
	}
}

// gateInputs builds the AnalyzeProgram inputs for the gate project, bridging
// beltgen's string ids to FileID.
func gateInputs() (map[FileID]*abstract.Document, map[FileID]map[*ast.UseDecl]FileID) {
	raw := beltgen.Documents(beltgen.Project(gateProject))
	docs := make(map[FileID]*abstract.Document, len(raw))
	for id, d := range raw {
		docs[FileID(id)] = d
	}
	uses := make(map[FileID]map[*ast.UseDecl]FileID, len(raw))
	for id, u := range beltgen.Uses(raw) {
		m := make(map[*ast.UseDecl]FileID, len(u))
		for decl, target := range u {
			m[decl] = FileID(target)
		}
		uses[FileID(id)] = m
	}
	return docs, uses
}
