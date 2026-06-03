package semantic

import (
	"reflect"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// This is a small demand-driven, memoizing query engine in the style of Salsa /
// rust-analyzer, specialised to the semantic queries (symbols, typeOf, value).
//
// Each query result is memoized with the dependencies it read and two revision
// stamps: changedAt (when its value last changed) and verifiedAt (when it was
// last confirmed up to date). On a new revision a query is re-verified by
// checking whether any dependency's value changed; if none did it is reused
// (early cutoff), and crucially, when a query is recomputed but produces the
// same value its changedAt is left untouched, so the change does not propagate
// to its dependents. Query keys are *ast.ConstDecl pointers, which the AST keeps
// stable for unedited declarations, so an edit only disturbs the declarations it
// actually touched and whatever transitively depended on their results.

type queryKind int

const (
	qInput   queryKind = iota // the AST file (the engine's only input)
	qSymbols                  // name -> declaration table
	qResolve                  // *ast.Identifier -> referent declaration
	qTypeOf                   // *ast.ConstDecl -> ir.Type
	qValue                    // *ast.ConstDecl -> *ir.Constant
)

// queryKey identifies a memoized computation: decl keys the per-declaration
// queries (typeOf, value), id keys resolve, and both are nil for the input and
// the symbol table. All fields are comparable, so the key works as a map key.
type queryKey struct {
	kind queryKind
	decl *ast.ConstDecl
	id   *ast.Identifier
}

var (
	inputKey   = queryKey{kind: qInput}
	symbolsKey = queryKey{kind: qSymbols}
)

func resolveKey(id *ast.Identifier) queryKey { return queryKey{kind: qResolve, id: id} }
func typeOfKey(decl *ast.ConstDecl) queryKey { return queryKey{kind: qTypeOf, decl: decl} }
func valueKey(decl *ast.ConstDecl) queryKey  { return queryKey{kind: qValue, decl: decl} }

type depEdge struct {
	key       queryKey
	changedAt int
}

type memo struct {
	value      any
	verifiedAt int
	changedAt  int
	deps       []depEdge
}

type frame struct {
	deps []depEdge
}

// database is the query engine state.
type database struct {
	revision       int
	file           *ast.File
	inputChangedAt int
	memos          map[queryKey]*memo
	stack          []*frame // active computations, for dependency capture
	running        map[queryKey]bool
	computed       map[queryKey]bool // keys (re)computed since the last setInput; for tests
}

func newDatabase() *database {
	return &database{
		memos:    map[queryKey]*memo{},
		running:  map[queryKey]bool{},
		computed: map[queryKey]bool{},
	}
}

// setInput installs a new AST file as the input and opens a new revision.
func (db *database) setInput(file *ast.File) {
	db.revision++
	db.file = file
	db.inputChangedAt = db.revision
	db.computed = map[queryKey]bool{}
}

// demand returns a query's value, ensuring its memo is valid for the current
// revision. It records nothing; use read to also capture a dependency.
func (db *database) demand(key queryKey) any {
	if key.kind == qInput {
		return db.file
	}
	if db.running[key] {
		return cycleValue(key) // re-entered while computing/verifying: a cycle
	}
	if m := db.memos[key]; m != nil && m.verifiedAt == db.revision {
		return m.value
	}

	db.running[key] = true
	defer delete(db.running, key)

	m := db.memos[key]
	if m != nil && db.verify(m) {
		m.verifiedAt = db.revision
		return m.value
	}

	f := &frame{}
	db.stack = append(db.stack, f)
	value := db.compute(key)
	db.stack = db.stack[:len(db.stack)-1]
	db.computed[key] = true

	changedAt := db.revision
	if m != nil && reflect.DeepEqual(m.value, value) {
		changedAt = m.changedAt // early cutoff: value unchanged
	}
	db.memos[key] = &memo{value: value, verifiedAt: db.revision, changedAt: changedAt, deps: f.deps}
	return value
}

// verify reports whether a memo's dependencies are all unchanged, so the memo
// can be reused without recomputing.
func (db *database) verify(m *memo) bool {
	for _, dep := range m.deps {
		db.demand(dep.key)
		if db.changedAtOf(dep.key) > dep.changedAt {
			return false
		}
	}
	return true
}

// read returns a query's value and, if a computation is in progress, records it
// as a dependency of that computation.
func (db *database) read(key queryKey) any {
	value := db.demand(key)
	if n := len(db.stack); n > 0 {
		top := db.stack[n-1]
		top.deps = append(top.deps, depEdge{key: key, changedAt: db.changedAtOf(key)})
	}
	return value
}

// changedAtOf is the revision a query's value last changed. A key currently
// being computed (a cycle) reports the current revision, so a dependent treats
// it as changed and the cyclic memo is recomputed rather than trusted.
func (db *database) changedAtOf(key queryKey) int {
	switch {
	case key.kind == qInput:
		return db.inputChangedAt
	case db.running[key]:
		return db.revision
	}
	if m := db.memos[key]; m != nil {
		return m.changedAt
	}
	return db.revision
}

// compute runs the query function for key, reading its dependencies through the
// engine so they are captured.
func (db *database) compute(key queryKey) any {
	switch key.kind {
	case qSymbols:
		return buildSymbols(db.read(inputKey).(*ast.File))
	case qResolve:
		syms := db.read(symbolsKey).(map[string]*ast.ConstDecl)
		return syms[key.id.Name]
	case qTypeOf:
		return computeType(key.decl, engineQueries{db})
	case qValue:
		return computeValue(key.decl, engineQueries{db})
	default:
		return nil
	}
}

// cycleValue is the fallback a query yields when a cycle is detected (an
// un-annotated reference depending on, transitively, its own type or value).
func cycleValue(key queryKey) any {
	switch key.kind {
	case qTypeOf:
		return ir.Invalid
	case qValue:
		return (*ir.Constant)(nil)
	case qResolve:
		return (*ast.ConstDecl)(nil)
	default:
		return nil
	}
}

// engineQueries is the memoizing implementation of the queries interface, used
// by Document; reads go through the database so dependencies are tracked.
type engineQueries struct {
	db *database
}

func (e engineQueries) resolve(id *ast.Identifier) *ast.ConstDecl {
	target, _ := e.db.read(resolveKey(id)).(*ast.ConstDecl)
	return target
}

func (e engineQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	return e.db.read(typeOfKey(decl)).(ir.Type)
}

func (e engineQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	v, _ := e.db.read(valueKey(decl)).(*ir.Constant)
	return v
}
