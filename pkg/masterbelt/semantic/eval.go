package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// graphFoldEnv is the eval.GraphEnv the post-check folds run in: a referenced
// constant reads the type-blind value query first and falls back to this
// file's published Eval — the late re-fold's monotone widening, exactly the
// reading resolvedEnv.ValueOf keeps — and type names resolve in the file's
// universe (a name lookup, never the type query).
type graphFoldEnv struct {
	q    queries
	file FileID
	own  map[*ast.ConstDecl]*ir.Const
}

func (e graphFoldEnv) ConstValue(c *ir.Const) *ir.Constant {
	if c.Syntax == nil {
		return nil
	}
	if v := e.q.valueOf(c.Syntax); v != nil {
		return v
	}
	if own := e.own[c.Syntax]; own != nil {
		return own.Eval
	}
	return nil
}
func (e graphFoldEnv) LookupType(name string) *ir.TypeDef { return e.q.universe(e.file)[name] }
func (e graphFoldEnv) Registry() *builtin.Registry        { return e.q.registry() }

// exprFolder folds source expressions for the in-walk diagnostics: each fold
// lowers the expression to its type-blind value graph through the constant
// binder and interprets it — the same two steps the value query takes — so
// the checks read exactly the semantics the folder publishes. The checks fold
// only constant values (a local or parameter does not fold), the conservative
// discipline they have always kept.
type exprFolder struct {
	q    queries
	file FileID
}

func (f exprFolder) binder(expected *ir.TypeDef) constBinder {
	return constBinder{q: f.q, file: f.file, irOf: f.q.constShellTable(), fnOf: f.q.funcShellTable(), expected: expected}
}

func (f exprFolder) env() graphFoldEnv { return graphFoldEnv{q: f.q, file: f.file} }

// fold lowers and folds one expression, with no expectation.
func (f exprFolder) fold(e ast.Expr) *ir.Constant {
	if e == nil {
		return nil
	}
	return eval.Graph(lower.Value(e, f.binder(nil)), f.env())
}

// memberFor resolves the member a folded expression flows into want as.
func (f exprFolder) memberFor(e ast.Expr, want ir.Type) ir.Type {
	if e == nil {
		return want
	}
	return eval.GraphMemberFor(lower.Value(e, f.binder(nil)), want, f.env())
}

// nodesBySyntax indexes a value graph's nodes by their syntax anchors, the
// outermost wrapper claiming each anchor (parents walk first) — the channel a
// per-sub-expression fold (the power-assert diagram) reads its nodes through.
func nodesBySyntax(root ir.Value) map[ast.Expr]ir.Value {
	out := map[ast.Expr]ir.Value{}
	ir.WalkValues(root, func(v ir.Value) bool {
		if syn := ir.SyntaxOf(v); syn != nil {
			if _, claimed := out[syn]; !claimed {
				out[syn] = v
			}
		}
		return true
	})
	return out
}

// computeValue is the evaluation rule, shared by both query implementations:
// the declaration's initializer is lowered to its (type-blind) value graph and
// folded by the IR interpreter. The file is the one decl sits in. The
// constant's resolved annotation type is the value folder's expectation
// channel: a bare member folds through its enum (lowered as an
// EnumMemberValue), an empty collection literal settles its mapness, and a
// member value is tagged with its union member. It is resolved here by a pure
// universe lookup (not the type query) so the value query stays independent
// of typeOf. The reachable files' function bodies are resolved first
// (funcsOf, a memoized, dependency-tracked point), so a call the graph binds
// applies a deterministic body whatever order the files assemble in.
func computeValue(file FileID, decl *ast.ConstDecl, q queries) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	for f := range q.reachableFrom(file) {
		q.funcsOf(f)
	}
	graph := lower.Value(decl.Value, constBinder{
		q: q, file: file,
		irOf: q.constShellTable(), fnOf: q.funcShellTable(),
		expected: annotationEnum(q, file, decl),
	})
	return eval.GraphExpecting(graph, annotationResolved(q, file, decl), graphFoldEnv{q: q, file: file})
}

// annotationResolved resolves a constant's type annotation to its full type
// through a pure name lookup in the file's universe — the channel value folding
// reads the enum, collection mapness, and union member from. It returns nil for
// an unannotated const, so the folder's expectation channels stay clear.
func annotationResolved(q queries, fileID FileID, decl *ast.ConstDecl) ir.Type {
	if decl.Type == nil {
		return nil
	}
	r := &infer.TypeResolver{Defs: q.universe(fileID), Qualified: qualifiedFrom(q, q.importsOf(fileID))}
	return r.ResolveType(decl.Type, nil)
}
