package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
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
func (e evalEnv) ValueOf(decl *ast.ConstDecl) *ir.Constant { return e.q.valueOf(decl) }
func (e evalEnv) Registry() *builtin.Registry              { return e.q.registry() }

// computeValue is the evaluation rule, shared by both query implementations.
// The file is the one decl sits in.
func computeValue(file FileID, decl *ast.ConstDecl, q queries) *ir.Constant {
	return eval.Decl(decl, evalEnv{q: q, file: file})
}
