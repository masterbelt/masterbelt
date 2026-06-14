// This file holds the type-declaration side of refinement: resolveWhere
// type-checks a declaration's where-clause predicate and keeps it on the
// definition only when it is a usable compile-time bool that folds. witness
// supplies a representative self value for the declaration-time probe fold, and
// predicateEnv is the eval environment such a predicate folds in (the registry
// alone, since a usable predicate references nothing but self and literals).

package semantic

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/lower"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// resolveWhere type-checks the declaration's refinement predicate — self is the
// underlying body type, so the comparisons type against the body's operators —
// and keeps it on the definition only when it is a usable compile-time
// predicate: a bool that folds. An unusable predicate is reported here, once,
// at the declaration; the definition's Where stays nil so the per-constant
// check never fires for it (the ir.Invalid style of suppression). The silent
// pass (nil at/diags) decides usability identically and just skips the
// reporting, so the memoized definitions and the diagnostics never disagree.
func resolveWhere(q queries, fileID FileID, r *infer.TypeResolver, reg *builtin.Registry, td *ast.TypeDecl, def *ir.TypeDef, at func(ast.Node) span, diags *diagnostic.List, res *callResolutions) {
	if td.Where == nil || def.Body == nil || ir.HasInvalid(def.Body) {
		return
	}
	report := at != nil && diags != nil
	var sink *infer.Sink
	before := 0
	if report {
		// The checking walk's facts stream into res (when the reporting pass
		// supplies one), keyed by the predicate's expressions — the same AST
		// the memoized definition's Where graph anchors to — so the write-back
		// types and adapts the predicate graph like any body's.
		sink = exprSink(at, diags, res)
		before = diags.Len()
	}
	// The predicate types in a body scope with no parameters: self and literals,
	// plus a method call on self. self is the nominal type being refined (not its
	// underlying body), so its own impl methods resolve — where self.isValid()
	// reads isValid on the type, the way a method body sees self's methods — while
	// the operator comparisons still derive from the underlying primitive. The
	// funcs the predicate may call are the same body funcs, so a self method's
	// body resolves its own calls.
	self := selfType(def)
	bs := infer.BodyScope{Reg: reg, Universe: r.Defs, Qualified: r.Qualified, Self: self}
	t := infer.CheckPredicate(td.Where, bs, sink)
	if t == ir.Invalid {
		// A predicate types to Invalid when a reference in it does not resolve. The
		// checking walk reports an operator error it sees, but a bare name that
		// resolves to nothing — or to a top-level constant, which a predicate may
		// not read (its vocabulary is self, literals, and self's own methods) —
		// types to Invalid with no diagnostic of its own. Left unreported the
		// refinement would be dropped (Where stays nil) and silently unenforced,
		// the master cell checks among the casualties, so it must be surfaced here.
		if report {
			// self is the refined nominal type, so a bare name reading one of its
			// members with self omitted — a field, a getter, or an implicit
			// self-method call (where ok(x)) — is a resolved self reference, not an
			// undefined name, the same exemption a master per-row check makes. The
			// callee set keeps the method exemption to a genuine call, not a bare
			// method name.
			callees := callCalleeIdents(td.Where)
			selfMember := func(id *ast.Identifier) bool { return selfReference(reg, self, id, callees) }
			reportRefIssues(fileID, td.Where, q, at, diags, nil, selfMember)
			if diags.Len() == before {
				// Neither the checking walk nor the reference check found anything to
				// report, so the predicate reads a resolvable value a refinement may
				// not use: it is not a usable compile-time predicate.
				s := at(td.Where)
				diags.Add(newRefinementNotConstantDiagnostic(s.offset, s.width))
			}
		}
		return
	}
	if !types.IsBoolean(reg, t) {
		if report {
			s := at(td.Where)
			diags.Add(newRefinementNotBoolDiagnostic(s.offset, s.width, t.String()))
		}
		return
	}
	// The predicate must fold. A witness value of the body type stands in for
	// self — the fold is value-independent for everything the type rules let
	// through (intrinsic-backed methods over self and literals, and a self-method
	// call whose body folds the same way), so a witness that folds proves every
	// constant's check will. selfDef is supplied so a self-method call resolves
	// its method on the type and folds its body; the predicate env resolves the
	// universe (for a nominal annotation in a self method's signature) without
	// the type query, keeping the value query type-independent.
	// The predicate is lowered to its resolved value graph — self bound to a
	// SelfValue, a self-method call to an ir.Call — the IR-only form every
	// fold of it runs on, the witness probe below included.
	graph := lower.Value(td.Where, bodyBinder{r: r, reg: reg, self: true, selfType: selfType(def)})
	env := predicateEnv{reg: reg, universe: r.Defs, qualified: r.Qualified}
	if v := eval.GraphPredicate(graph, witness(reg, def.Body), def, env); v == nil || v.Kind != ir.ConstBool {
		if report {
			s := at(td.Where)
			diags.Add(newRefinementNotConstantDiagnostic(s.offset, s.width))
		}
		return
	}
	def.Where = graph
}

// witness is a representative constant of t for the declaration-time probe
// fold: 1 for an integer (avoiding a divide-by-self zero), true for a boolean,
// the empty string for a string, nil — never foldable — for anything else.
func witness(reg *builtin.Registry, t ir.Type) *ir.Constant {
	switch {
	case types.IsInteger(reg, t):
		return ir.IntConstant(big.NewInt(1))
	case types.IsBoolean(reg, t):
		return ir.BoolConstant(true)
	case types.IsString(reg, t):
		return ir.StringConstant("")
	default:
		return nil
	}
}

// selfType is the type the self keyword has in a refinement predicate: the
// nominal type being refined, so a self-method call resolves the method on the
// type while the operator comparisons still derive from the underlying
// primitive. A bare primitive body (a type alias with no definition of its own)
// has no nominal wrapper, so self stays the body type.
func selfType(def *ir.TypeDef) ir.Type {
	if def == nil {
		return nil
	}
	return &ir.Named{Def: def}
}

// predicateEnv is the fold environment of a refinement predicate: the
// registry, and the type universe a name in the predicate's graph resolves
// against. The type rules guarantee the predicate references no constant —
// only self, literals, and self's own methods — so ConstValue never needs to
// find anything.
type predicateEnv struct {
	reg       *builtin.Registry
	universe  map[string]*ir.TypeDef
	qualified func(namespace, name string) *ir.TypeDef
}

func (e predicateEnv) ConstValue(*ir.Const) *ir.Constant { return nil }
func (e predicateEnv) LookupType(name string) *ir.TypeDef {
	if e.universe != nil {
		if d, ok := e.universe[name]; ok {
			return d
		}
	}
	d, _ := e.reg.Lookup(name)
	return d
}
func (e predicateEnv) Registry() *builtin.Registry { return e.reg }
