// This file implements the evaluator's relation-folder seam (eval.RelationFolder)
// for the master data layer: given a relation aggregate the evaluator reaches while
// folding a per-table check — a count() or sum() over a master's relation, already
// inlined to a self-contained chain — it runs the query against the loaded rows. The
// evaluator interprets the body the query sits in (a static fn's lets and
// arithmetic, a helper, a conditional); this only runs the query, through the query
// driver (pkg/master/sql) and the in-memory SQLite engine (pkg/master/sqlite).

package load

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/master"
	mastersql "github.com/masterbelt/masterbelt/pkg/master/sql"
	"github.com/masterbelt/masterbelt/pkg/master/sqlite"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// relationFold folds the relation aggregates of one master's per-table checks: the
// engine its rows are loaded into (nil when the table failed to load, so only an
// unfiltered count — which needs no cells — still folds), the loaded row count for
// that unfiltered case, the master the check is on (a query over another master is
// not run against this engine), and the fold environment that resolves a where
// predicate's named constants.
type relationFold struct {
	eng    *sqlite.Engine
	rows   int64
	master *ir.TypeDef
	env    eval.GraphEnv
	// full is the complete typed table — every column, not just the int64-safe
	// subset the engine stores — so a materialized row (to_list) carries all the
	// master's fields. The engine selects which rows by their synthetic key (their
	// index); this rebuilds the full rows from those keys.
	full master.Table
}

// relationEnv is the fold environment the evaluator drives a per-table check in: the
// base environment plus the relation folder, so the evaluator (which type-asserts
// its env for eval.RelationFolder) reaches the master's rows for a count/sum while
// folding the surrounding body the ordinary way.
type relationEnv struct {
	eval.GraphEnv
	fold relationFold
}

// FoldRelationAggregate runs a count()/sum() over the master relation against the
// loaded rows, returning the integer value, or ok=false when the chain is not such
// an aggregate, is over a different master, or carries a predicate the driver cannot
// express — in which case the evaluator leaves it unfoldable and the check fails
// safe.
func (e relationEnv) FoldRelationAggregate(chain ir.Value) (*ir.Constant, bool) {
	if rel, m, unsupported, ok := mastersql.CountRelation(chain, e.fold.env); ok {
		return e.fold.count(rel, m, chain, unsupported)
	}
	if rel, col, m, unsupported, ok := mastersql.SumRelation(chain, e.fold.env); ok {
		return e.fold.sum(rel, col, m, unsupported)
	}
	if rel, m, unsupported, ok := mastersql.RowsRelation(chain, e.fold.env); ok {
		return e.fold.toList(rel, m, unsupported)
	}
	return nil, false
}

// toList materializes a recognized to_list query: the engine selects the synthetic
// keys of the rows the filter keeps (capped by the limit, in key order), and the
// full rows are rebuilt from the typed table by those keys as a list of row records.
// A query over another master, an unsupported predicate, or an unloaded table is not
// driven — the materialization is left unfoldable and the check fails safe.
func (f relationFold) toList(rel mastersql.Relation, m *ir.TypeDef, unsupported []mastersql.Unsupported) (*ir.Constant, bool) {
	if m != f.master || len(unsupported) > 0 || f.eng == nil {
		return nil, false
	}
	keys, err := f.eng.RowKeys(rel)
	if err != nil {
		return nil, false
	}
	entries := make([]ir.ConstEntry, 0, len(keys))
	for _, idx := range keys {
		if idx < 0 || idx >= len(f.full.Rows) {
			return nil, false
		}
		rec, _, ok := rowConstant(f.full.Columns, f.full.Rows[idx])
		if !ok {
			return nil, false
		}
		entries = append(entries, ir.ConstEntry{Value: rec})
	}
	return ir.CollectionConstantOf(entries, ir.CollList), true
}

// count runs a recognized count query: a query over another master is not driven
// against this engine; an unsupported predicate is a rejection; an unfiltered count
// reads the row count (no cells); a filtered count needs the engine.
func (f relationFold) count(rel mastersql.Relation, m *ir.TypeDef, chain ir.Value, unsupported []mastersql.Unsupported) (*ir.Constant, bool) {
	if m != f.master || len(unsupported) > 0 {
		return nil, false
	}
	if isBareCount(chain) {
		return ir.IntConstant(big.NewInt(f.rows)), true
	}
	if f.eng == nil {
		return nil, false
	}
	n, err := f.eng.Count(rel)
	if err != nil {
		return nil, false
	}
	return ir.IntConstant(big.NewInt(n)), true
}

// sum runs a recognized sum query against the engine, with the same cross-master,
// unsupported, and unloaded-table guards count uses.
func (f relationFold) sum(rel mastersql.Relation, col string, m *ir.TypeDef, unsupported []mastersql.Unsupported) (*ir.Constant, bool) {
	if m != f.master || len(unsupported) > 0 || f.eng == nil {
		return nil, false
	}
	total, err := f.eng.Sum(rel, col)
	if err != nil {
		return nil, false
	}
	return ir.IntConstant(total), true
}

// isBareCount reports whether a chain is count() directly over the master relation,
// with no where narrowing — the count of every row, which the row count answers
// without reading a cell. A narrowed count (where(...).count()) is not bare and
// needs the engine.
func isBareCount(chain ir.Value) bool {
	count, ok := chain.(*ir.Call)
	if !ok || count.Method != "count" {
		return false
	}
	_, ok = stripAdapt(count.Receiver).(*ir.MasterRelation)
	return ok
}

// stripAdapt peels the implicit-adaption wrappers off a value, so a chain reads
// through them.
func stripAdapt(v ir.Value) ir.Value {
	for {
		a, ok := v.(*ir.Adapt)
		if !ok {
			return v
		}
		v = a.Value
	}
}
