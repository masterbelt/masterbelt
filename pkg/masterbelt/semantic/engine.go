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
	qInput    queryKind = iota // a file's AST + resolved use targets (the engine's only inputs)
	qSymbols                   // a file's name -> declaration table
	qResolve                   // *ast.Identifier -> referent declaration
	qTypeOf                    // *ast.ConstDecl -> ir.Type
	qValue                     // *ast.ConstDecl -> *ir.Constant
	qTypeDefs                  // a file's resolved type definitions (and its annotation universe)
	qExports                   // a file's public surface: pub decls + pub use re-exports
	qImports                   // a file's import bindings: selective/wildcard names + namespaces
)

// queryKey identifies a memoized computation. The per-declaration queries
// (typeOf, value) are keyed by decl alone and resolve by id — AST pointers are
// globally unique, even across files — so only the per-file queries (input,
// symbols, typeDefs, exports, imports) and resolve (whose scope is the file
// the identifier sits in) carry the file dimension. All fields are comparable,
// so the key works as a map key.
type queryKey struct {
	kind queryKind
	file FileID
	decl *ast.ConstDecl
	id   *ast.Identifier
}

func inputKey(file FileID) queryKey    { return queryKey{kind: qInput, file: file} }
func symbolsKey(file FileID) queryKey  { return queryKey{kind: qSymbols, file: file} }
func typeDefsKey(file FileID) queryKey { return queryKey{kind: qTypeDefs, file: file} }
func exportsKey(file FileID) queryKey  { return queryKey{kind: qExports, file: file} }
func importsKey(file FileID) queryKey  { return queryKey{kind: qImports, file: file} }

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

// fileInput is one file's engine input: its syntax together with where the
// project layer resolved its imports. The two change together — retargeting a
// use without editing the file (a sibling appearing on disk) is still an input
// change.
type fileInput struct {
	file *ast.File
	uses map[*ast.UseDecl]FileID
}

// database is the query engine state.
type database struct {
	revision       int
	files          map[FileID]fileInput
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
		files:          map[FileID]fileInput{},
		declFile:       map[*ast.ConstDecl]FileID{},
		inputChangedAt: map[FileID]int{},
		memos:          map[queryKey]*memo{},
		running:        map[queryKey]bool{},
		computed:       map[queryKey]bool{},
	}
}

// setInput installs a new AST and resolved use targets for one file and opens
// a new revision. Other files' inputs keep their change stamps, so queries
// that read only them re-verify cheaply.
func (db *database) setInput(id FileID, file *ast.File, uses map[*ast.UseDecl]FileID) {
	db.revision++
	db.files[id] = fileInput{file: file, uses: uses}
	db.inputChangedAt[id] = db.revision
	db.computed = map[queryKey]bool{}
	db.reindexDecls()
}

// dropInput removes a file that has left the project and opens a new revision.
func (db *database) dropInput(id FileID) {
	db.revision++
	delete(db.files, id)
	delete(db.inputChangedAt, id)
	db.computed = map[queryKey]bool{}
	db.reindexDecls()
}

// reindexDecls rebuilds the declaration-to-file index after an input change.
// Unedited declarations keep their AST pointers, so entries are mostly
// rewritten in place; the rebuild is linear in the program and never retains
// declarations that have left the trees.
func (db *database) reindexDecls() {
	db.declFile = make(map[*ast.ConstDecl]FileID, len(db.declFile))
	for id, in := range db.files {
		if in.file == nil {
			continue
		}
		for _, d := range in.file.Decls {
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
		// Early cutoff: the value is unchanged. Keep the old object, not just
		// the old stamp — values containing pointers (the type definitions a
		// Named's identity hangs on) must stay the same objects, or memos
		// recorded before this recompute and queries computed after it would
		// hold different pointers for the same fact.
		changedAt = m.changedAt
		value = m.value
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
		in, _ := db.read(inputKey(key.file)).(fileInput)
		return buildSymbols(in.file)
	case qResolve:
		// Locals shadow imports; an imported name claimed by two or more
		// imports resolves to nothing and is flagged ambiguous.
		syms := db.read(symbolsKey(key.file)).(map[string]*ast.ConstDecl)
		if d := syms[key.id.Name]; d != nil {
			return resolution{decl: d}
		}
		imp, _ := db.read(importsKey(key.file)).(importTable)
		if b, ok := imp.values[key.id.Name]; ok {
			if b.ambiguous {
				return resolution{ambiguous: true}
			}
			return resolution{decl: b.target}
		}
		return resolution{}
	case qTypeOf:
		return infer.Decl(key.decl, typeEnv{q: engineQueries{db}, file: db.declFile[key.decl]})
	case qValue:
		return computeValue(db.declFile[key.decl], key.decl, engineQueries{db})
	case qTypeDefs:
		return db.computeTypeDefs(key.file)
	case qExports:
		return db.computeExports(key.file)
	case qImports:
		return db.computeImports(key.file)
	default:
		return nil
	}
}

// cycleValue is the fallback a query yields when a cycle is detected: an
// un-annotated reference depending on, transitively, its own type or value, or
// a module cycle re-entering its own exports (which assemble reports as
// cyclic_module).
func cycleValue(key queryKey) any {
	switch key.kind {
	case qTypeOf:
		return ir.Invalid
	case qValue:
		return (*ir.Constant)(nil)
	case qResolve:
		return resolution{}
	case qTypeDefs:
		return typeDefs{}
	case qExports:
		return exports{}
	case qImports:
		return importTable{}
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
	r, _ := e.db.read(resolveKey(file, id)).(resolution)
	if r.ambiguous {
		return nil
	}
	return r.decl
}

func (e engineQueries) ambiguousImport(file FileID, id *ast.Identifier) bool {
	r, _ := e.db.read(resolveKey(file, id)).(resolution)
	return r.ambiguous
}

func (e engineQueries) resolveMember(file FileID, m *ast.MemberExpr) *ast.ConstDecl {
	return resolveMemberThrough(e, file, m)
}

func (e engineQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	return e.db.read(typeOfKey(decl)).(ir.Type)
}

func (e engineQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	v, _ := e.db.read(valueKey(decl)).(*ir.Constant)
	return v
}

func (e engineQueries) typeDefs(file FileID) []*ir.TypeDef {
	td, _ := e.db.read(typeDefsKey(file)).(typeDefs)
	return td.list
}

func (e engineQueries) universe(file FileID) map[string]*ir.TypeDef {
	td, _ := e.db.read(typeDefsKey(file)).(typeDefs)
	return td.universe
}

func (e engineQueries) exportsOf(file FileID) exports {
	exp, _ := e.db.read(exportsKey(file)).(exports)
	return exp
}

func (e engineQueries) importsOf(file FileID) importTable {
	t, _ := e.db.read(importsKey(file)).(importTable)
	return t
}

func (e engineQueries) usesOf(file FileID) map[*ast.UseDecl]FileID {
	in, _ := e.db.read(inputKey(file)).(fileInput)
	return in.uses
}

func (e engineQueries) registry() *builtin.Registry { return e.db.reg }
