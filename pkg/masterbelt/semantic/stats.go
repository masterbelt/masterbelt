// This file is the engine's performance side-channel (D-1 M1): per-revision
// counters of how many distinct queries of each kind were recomputed versus
// reused (served from a verified memo). It is the executable measure of early
// cutoff — the engine's defining property — turned into a number a test can
// pin and an agent can read after an edit.
//
// The cardinal rule (D-1 §0, §8-2): instrumentation must not perturb what it
// measures. These counters live beside database.computed, never inside a
// memo's value and never consulted by equalValue — a stat folded into the
// cutoff comparison would make every query look changed and destroy the
// incrementality it is meant to observe. They reset with computed on each new
// revision, so Stats() reports exactly the last Refresh's work.

package semantic

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
	return "queryKind(" + sortableInt(int(k)) + ")"
}

// sortableInt renders n in decimal without pulling in strconv at this leaf.
func sortableInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Stats is a snapshot of one revision's query work: per-kind counts of the
// distinct queries recomputed and reused, with the totals. It is the M-reuse
// metric — the hard-gate signal of D-1 — in a form both a golden test and the
// --stats CLI output read.
type Stats struct {
	Computed map[string]int `json:"computed"`
	Reused   map[string]int `json:"reused"`
	// TotalComputed and TotalReused sum the per-kind counts, so a reader sees
	// the headline reuse ratio without re-summing.
	TotalComputed int `json:"totalComputed"`
	TotalReused   int `json:"totalReused"`
}

// stats derives the snapshot from the revision's computed and reused key sets.
// It counts distinct keys per kind: the same query demanded twice in one
// revision is one fact, recomputed or reused once.
func (db *database) stats() Stats {
	s := Stats{Computed: map[string]int{}, Reused: map[string]int{}}
	for key := range db.computed {
		s.Computed[key.kind.String()]++
		s.TotalComputed++
	}
	for key := range db.reused {
		s.Reused[key.kind.String()]++
		s.TotalReused++
	}
	return s
}
