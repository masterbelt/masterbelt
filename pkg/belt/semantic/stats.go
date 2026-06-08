// This file is the engine's performance side-channel: per-revision
// counters of how many distinct queries of each kind were recomputed versus
// reused (served from a verified memo). It is the executable measure of early
// cutoff — the engine's defining property — turned into a number a test can
// pin and an agent can read after an edit.
//
// The cardinal rule: instrumentation must not perturb what it
// measures. These counters live beside database.computed, never inside a
// memo's value and never consulted by equalValue — a stat folded into the
// cutoff comparison would make every query look changed and destroy the
// incrementality it is meant to observe. They reset with computed on each new
// revision, so Stats() reports exactly the last Refresh's work.

package semantic

import "strconv"

// kindNames maps each query kind to the stable label its stats and snapshots
// carry. A kind added to the enum without a name here renders as its number,
// which the completeness test below rejects.
var kindNames = map[queryKind]string{
	qInput:       "input",
	qSymbols:     "symbols",
	qResolve:     "resolve",
	qFuncSymbols: "funcSymbols",
	qResolveFunc: "resolveFunc",
	qTypeOf:      "typeOf",
	qValue:       "value", //nolint:goconst // a query-kind label table: each label is inline once, no shared constant
	qTypeDefs:    "typeDefs",
	qFuncs:       "funcs",
	qExports:     "exports",
	qImports:     "imports",
	qReachable:   "reachable",
	qModule:      "module",
}

// String returns the query kind's stable label.
func (k queryKind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "queryKind(" + strconv.Itoa(int(k)) + ")"
}

// Stats is a snapshot of one revision's query work: per-kind counts of the
// distinct queries recomputed and reused, with the totals. It is the reuse
// metric — the engine's hard-gate signal — in a form both a golden test and the
// --stats CLI output read.
type Stats struct {
	Computed map[string]int `json:"computed"`
	Reused   map[string]int `json:"reused"`
	// TotalComputed and TotalReused sum the per-kind counts, so a reader sees
	// the headline reuse ratio without re-summing.
	TotalComputed int `json:"totalComputed"`
	TotalReused   int `json:"totalReused"`
}

// memoCount is the number of live memo entries in the engine — the size of the
// memo table. It is a pure read of len(db.memos) and touches nothing the engine
// memoizes (no determinism impact); the LSP samples it over a long session as
// the leak signal: monotonic growth = a memo table that never sheds.
func (db *database) memoCount() int { return len(db.memos) }

// stats derives the snapshot from the revision's recompute key set and reuse
// counter. Computed counts distinct keys per kind (the same query demanded
// twice in one revision is one fact); reused is already a per-kind count of
// distinct keys, since a key reaches the verify-success mark at most once per
// revision (a later demand returns at the verified fast path).
func (db *database) stats() Stats {
	s := Stats{Computed: map[string]int{}, Reused: map[string]int{}}
	for key := range db.computed {
		s.Computed[key.kind.String()]++
		s.TotalComputed++
	}
	for kind, n := range db.reused {
		s.Reused[kind.String()] += n
		s.TotalReused += n
	}
	return s
}
