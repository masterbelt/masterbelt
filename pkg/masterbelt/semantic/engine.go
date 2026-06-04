package semantic

import (
	"reflect"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
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

// FileID identifies a file within the analyzed program: its normalized,
// "/"-separated path relative to the project root. It mirrors pkg/project's
// FileID — the two stay distinct types so the compiler core never imports the
// project layer; the binding layers (CLI, LSP) convert between them.
type FileID string

// soleFileID is the identity a single-file analysis gives its one file. A
// standalone Document (and the ad-hoc single-file paths) analyzes exactly one
// file, whose name does not matter; multi-file analysis keys files by their
// real project FileIDs.
const soleFileID FileID = ""

type queryKind int

const (
	qInput   queryKind = iota // a file's AST (the engine's only inputs)
	qSymbols                  // a file's name -> declaration table
	qResolve                  // *ast.Identifier -> referent declaration
	qTypeOf                   // *ast.ConstDecl -> ir.Type
	qValue                    // *ast.ConstDecl -> *ir.Constant
)

// queryKey identifies a memoized computation. The per-declaration queries
// (typeOf, value) are keyed by decl alone and resolve by id — AST pointers are
// globally unique, even across files — so only the per-file queries (input,
// symbols) and resolve (whose scope is the file the identifier sits in) carry
// the file dimension. All fields are comparable, so the key works as a map key.
type queryKey struct {
	kind queryKind
	file FileID
	decl *ast.ConstDecl
	id   *ast.Identifier
}

func inputKey(file FileID) queryKey   { return queryKey{kind: qInput, file: file} }
func symbolsKey(file FileID) queryKey { return queryKey{kind: qSymbols, file: file} }

func resolveKey(file FileID, id *ast.Identifier) queryKey {
	return queryKey{kind: qResolve, file: file, id: id}
}
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
	files          map[FileID]*ast.File
	declFile       map[*ast.ConstDecl]FileID // which file each declaration sits in
	reg            *builtin.Registry
	inputChangedAt map[FileID]int
	memos          map[queryKey]*memo
	stack          []*frame // active computations, for dependency capture
	running        map[queryKey]bool
	computed       map[queryKey]bool // keys (re)computed since the last setInput; for tests
}

func newDatabase(reg *builtin.Registry) *database {
	return &database{
		reg:            reg,
		files:          map[FileID]*ast.File{},
		declFile:       map[*ast.ConstDecl]FileID{},
		inputChangedAt: map[FileID]int{},
		memos:          map[queryKey]*memo{},
		running:        map[queryKey]bool{},
		computed:       map[queryKey]bool{},
	}
}

// setInput installs a new AST for one file and opens a new revision. Other
// files' inputs keep their change stamps, so queries that read only them
// re-verify cheaply.
func (db *database) setInput(id FileID, file *ast.File) {
	db.revision++
	db.files[id] = file
	db.inputChangedAt[id] = db.revision
	db.computed = map[queryKey]bool{}
	db.reindexDecls()
}

// reindexDecls rebuilds the declaration-to-file index after an input change.
// Unedited declarations keep their AST pointers, so entries are mostly
// rewritten in place; the rebuild is linear in the program and never retains
// declarations that have left the trees.
func (db *database) reindexDecls() {
	db.declFile = make(map[*ast.ConstDecl]FileID, len(db.declFile))
	for id, f := range db.files {
		for _, d := range f.Decls {
			db.declFile[d] = id
		}
	}
}

// demand returns a query's value, ensuring its memo is valid for the current
// revision. It records nothing; use read to also capture a dependency.
func (db *database) demand(key queryKey) any {
	if key.kind == qInput {
		return db.files[key.file]
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
		return db.inputChangedAt[key.file]
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
		file, _ := db.read(inputKey(key.file)).(*ast.File)
		return buildSymbols(file)
	case qResolve:
		syms := db.read(symbolsKey(key.file)).(map[string]*ast.ConstDecl)
		return syms[key.id.Name]
	case qTypeOf:
		return infer.Decl(key.decl, typeEnv{q: engineQueries{db}, file: db.declFile[key.decl]})
	case qValue:
		return computeValue(db.declFile[key.decl], key.decl, engineQueries{db})
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

func (e engineQueries) resolve(file FileID, id *ast.Identifier) *ast.ConstDecl {
	target, _ := e.db.read(resolveKey(file, id)).(*ast.ConstDecl)
	return target
}

func (e engineQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	return e.db.read(typeOfKey(decl)).(ir.Type)
}

func (e engineQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	v, _ := e.db.read(valueKey(decl)).(*ir.Constant)
	return v
}

func (e engineQueries) registry() *builtin.Registry { return e.db.reg }
