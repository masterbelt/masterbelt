package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// evalEnv adapts the semantic query interface to eval.Env, so constant folding
// in package eval reads resolution and referenced values through the same
// memoizing engine (which tracks dependencies and breaks cycles). It carries
// the file whose scope identifiers resolve in, so eval.Env stays file-blind.
type evalEnv struct {
	q    queries
	file FileID
}

func (e evalEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(e.file, id) }
func (e evalEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl {
	return e.q.resolveMember(e.file, m)
}
func (e evalEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl {
	return e.q.resolveFunc(e.file, id)
}
func (e evalEnv) ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl {
	return e.q.resolveFuncMember(e.file, m)
}
func (e evalEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant { return e.q.valueOf(decl) }
func (e evalEnv) LookupType(name string) *ir.TypeDef       { return e.q.universe(e.file)[name] }
func (e evalEnv) Registry() *builtin.Registry              { return e.q.registry() }

// TypeExprDef resolves a written type annotation to the type definition it names
// — the syntactic type channel a nominal-typed method receiver folds through. It
// is a pure name lookup in the file's universe (the same TypeResolver
// annotationEnum uses, minus the enum filter), so the value query stays
// independent of typeOf; a union, record, function, or primitive annotation
// (anything but a single nominal name) yields nil. It satisfies
// eval.ReceiverTyper.
func (e evalEnv) TypeExprDef(t ast.TypeExpr) *ir.TypeDef {
	if t == nil {
		return nil
	}
	r := &infer.TypeResolver{Defs: e.q.universe(e.file), Qualified: qualifiedFrom(e.q, e.q.importsOf(e.file))}
	return nominalDefOf(r.ResolveType(t, nil))
}

// TypeExprType resolves a written annotation to its full type — a record
// annotation yielding an *ir.Record — through the same universe resolution
// TypeExprDef uses, minus the nominal-only filter. It is the channel a record
// field receiver's static type folds through; like TypeExprDef it is a pure
// universe lookup, never the typeOf query. It satisfies eval.ReceiverTyper.
func (e evalEnv) TypeExprType(t ast.TypeExpr) ir.Type {
	if t == nil {
		return nil
	}
	r := &infer.TypeResolver{Defs: e.q.universe(e.file), Qualified: qualifiedFrom(e.q, e.q.importsOf(e.file))}
	return r.ResolveType(t, nil)
}

// nominalDefOf returns the definition behind a nominal (or applied generic)
// type, or nil for any other type form. It is the def a method table is read
// from — a Named or an App carries one; a union, record, function, primitive, or
// invalid type does not.
func nominalDefOf(t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Named:
		return t.Def
	case *ir.App:
		return t.Def
	}
	return nil
}

// computeValue is the evaluation rule, shared by both query implementations.
// The file is the one decl sits in. A bare member in the initializer folds
// through the annotation's enum, resolved here by a pure universe lookup (not
// the type query) so the value query stays independent of typeOf.
func computeValue(file FileID, decl *ast.ConstDecl, q queries) *ir.Constant {
	return eval.DeclExpecting(decl, annotationEnum(q, file, decl), evalEnv{q: q, file: file})
}
