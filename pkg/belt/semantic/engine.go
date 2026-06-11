package semantic

import (
	"maps"
	"reflect"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
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

// soleFileID is the identity a single-file analysis gives its one file —
// Analyze (the oracle) and ad-hoc single-file paths analyze exactly one file,
// whose name does not matter; multi-file analysis keys files by their real
// project FileIDs.
const soleFileID FileID = ""

type queryKind int

const (
	qInput       queryKind = iota // a file's AST + resolved use targets (the engine's only inputs)
	qSymbols                      // a file's name -> declaration table
	qResolve                      // *ast.Identifier -> referent declaration
	qFuncSymbols                  // a file's name -> function declaration table
	qResolveFunc                  // *ast.Identifier -> referent function declaration (a call's callee)
	qTypeOf                       // *ast.ConstDecl -> ir.Type
	qValue                        // *ast.ConstDecl -> *ir.Constant
	qTypeDefs                     // a file's resolved type definitions (and its annotation universe)
	qFuncs                        // a file's resolved top-level functions (signatures + lowered bodies on the shells)
	qExports                      // a file's public surface: pub decls + pub use re-exports
	qImports                      // a file's import bindings: selective/wildcard names + namespaces
	qReachable                    // the files a file's use graph reaches (itself included)
	qModule                       // a file's assembled IR module and diagnostics
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

func inputKey(file FileID) queryKey       { return queryKey{kind: qInput, file: file} }
func symbolsKey(file FileID) queryKey     { return queryKey{kind: qSymbols, file: file} }
func funcSymbolsKey(file FileID) queryKey { return queryKey{kind: qFuncSymbols, file: file} }
func typeDefsKey(file FileID) queryKey    { return queryKey{kind: qTypeDefs, file: file} }
func funcsKey(file FileID) queryKey       { return queryKey{kind: qFuncs, file: file} }
func exportsKey(file FileID) queryKey     { return queryKey{kind: qExports, file: file} }
func importsKey(file FileID) queryKey     { return queryKey{kind: qImports, file: file} }
func reachableKey(file FileID) queryKey   { return queryKey{kind: qReachable, file: file} }
func moduleKey(file FileID) queryKey      { return queryKey{kind: qModule, file: file} }

func resolveKey(file FileID, id *ast.Identifier) queryKey {
	return queryKey{kind: qResolve, file: file, id: id}
}

func resolveFuncKey(file FileID, id *ast.Identifier) queryKey {
	return queryKey{kind: qResolveFunc, file: file, id: id}
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

// assembly is the value of a module query: one file's assembled IR module
// and diagnostics. Assembling is the outer pass over the memoized facts;
// memoizing it per file turns an unchanged file's refresh into a single
// verification walk over what its last assembly read.
type assembly struct {
	module *ir.Module
	diags  []diagnostic.Diagnostic
}

// database is the query engine state.
type database struct {
	revision       int
	files          map[FileID]fileInput
	declFile       map[*ast.ConstDecl]FileID      // which file each declaration sits in
	typeFile       map[ast.Node]FileID            // which file each type declaration sits in (type/enum/interface/master, keyed by the declaration node)
	shells         map[*ast.ConstDecl]*ir.Const   // the identity ir.Const of every declaration
	fnShells       map[*ast.FuncDecl]*ir.Function // the identity ir.Function of every function
	reg            *builtin.Registry
	prelude        map[string]*ir.TypeDef // the implicit base tier: the prelude file's exported types
	inputChangedAt map[FileID]int
	memos          map[queryKey]*memo
	stack          []*frame // active computations, for dependency capture
	running        map[queryKey]bool
	computed       map[queryKey]bool // keys (re)computed since the last setInput; for tests
	reused         map[queryKind]int // per-kind count of queries served from a verified memo since the last setInput (the reuse side-channel)
}

func newDatabase(u builtins) *database {
	return &database{
		reg:            u.reg,
		prelude:        u.prelude,
		files:          map[FileID]fileInput{},
		declFile:       map[*ast.ConstDecl]FileID{},
		typeFile:       map[ast.Node]FileID{},
		shells:         map[*ast.ConstDecl]*ir.Const{},
		fnShells:       map[*ast.FuncDecl]*ir.Function{},
		inputChangedAt: map[FileID]int{},
		memos:          map[queryKey]*memo{},
		running:        map[queryKey]bool{},
		computed:       map[queryKey]bool{},
		reused:         map[queryKind]int{},
	}
}

// setInput installs a new AST and resolved use targets for one file and opens
// a new revision. An input that did not actually change (the same syntax tree,
// the same use targets) is a no-op, keeping its change stamp — callers may
// re-push a whole project after one file's edit, and the untouched siblings
// must stay verifiable cheaply. Other files' inputs keep their change stamps
// regardless, so queries that read only them re-verify cheaply.
func (db *database) setInput(id FileID, file *ast.File, uses map[*ast.UseDecl]FileID) {
	old, known := db.files[id]
	if known && old.file == file && maps.Equal(old.uses, uses) {
		return
	}
	db.revision++
	db.files[id] = fileInput{file: file, uses: uses}
	db.inputChangedAt[id] = db.revision
	db.computed = map[queryKey]bool{}
	db.reused = map[queryKind]int{}
	db.rebindDecls(id, old.file, file)
}

// dropInput removes a file that has left the project and opens a new revision.
func (db *database) dropInput(id FileID) {
	old, known := db.files[id]
	if !known {
		return
	}
	db.revision++
	delete(db.files, id)
	delete(db.inputChangedAt, id)
	db.computed = map[queryKey]bool{}
	db.reused = map[queryKind]int{}
	db.rebindDecls(id, old.file, nil)
}

// rebindDecls updates the declaration-to-file index and the shell table for
// one file's input change: declarations that left the tree drop their entries,
// new ones enter, and unedited declarations — whose AST pointers the old and
// new trees share — keep their shells, so references across files (and across
// refreshes) keep binding the same ir.Const objects.
func (db *database) rebindDecls(id FileID, old, fresh *ast.File) {
	keep := map[*ast.ConstDecl]bool{}
	keepFuncs := map[*ast.FuncDecl]bool{}
	if fresh != nil {
		module := moduleSegment(id)
		for _, d := range fresh.Decls {
			keep[d] = true
			db.declFile[d] = id
			if db.shells[d] == nil {
				db.shells[d] = &ir.Const{Name: d.Name, Anchor: declAnchor(module, d.Name), Public: d.Public, Doc: d.Doc, Syntax: d}
			}
		}
		for _, fd := range fresh.Funcs {
			keepFuncs[fd] = true
			if db.fnShells[fd] == nil {
				db.fnShells[fd] = &ir.Function{Name: fd.Name, Anchor: declAnchor(module, fd.Name), Public: fd.Public, Doc: fd.Doc, Syntax: fd}
			}
		}
	}
	db.rebindTypeDecls(id, old, fresh)
	if old != nil {
		for _, d := range old.Decls {
			if !keep[d] {
				delete(db.declFile, d)
				delete(db.shells, d)
			}
		}
		for _, fd := range old.Funcs {
			if !keepFuncs[fd] {
				delete(db.fnShells, fd)
			}
		}
	}
}

// rebindTypeDecls re-files a refreshed file's type-like declarations — type,
// enum, interface, and master — by their declaration node: each fresh one is
// filed under id, and one the new tree dropped is unfiled, so FileOfType stays
// exact across an edit. The node keys match TypeDef.DeclSyntax, the one accessor
// that knows where each kind keeps its backpointer.
func (db *database) rebindTypeDecls(id FileID, old, fresh *ast.File) {
	keep := map[ast.Node]bool{}
	file := func(n ast.Node) {
		keep[n] = true
		db.typeFile[n] = id
	}
	if fresh != nil {
		for _, t := range fresh.Types {
			file(t)
		}
		for _, e := range fresh.Enums {
			file(e)
		}
		for _, in := range fresh.Interfaces {
			file(in)
		}
		for _, m := range fresh.Masters {
			file(m)
		}
	}
	if old == nil {
		return
	}
	unfile := func(n ast.Node) {
		if !keep[n] {
			delete(db.typeFile, n)
		}
	}
	for _, t := range old.Types {
		unfile(t)
	}
	for _, e := range old.Enums {
		unfile(e)
	}
	for _, in := range old.Interfaces {
		unfile(in)
	}
	for _, m := range old.Masters {
		unfile(m)
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
		db.reused[key.kind]++ // a memo carried across the revision: early cutoff worked
		return m.value
	}

	f := &frame{}
	db.stack = append(db.stack, f)
	value := db.compute(key)
	db.stack = db.stack[:len(db.stack)-1]
	db.computed[key] = true

	changedAt := db.revision
	if m != nil && equalValue(key.kind, m.value, value) {
		// Early cutoff: the value is unchanged. Keep the old object, not just
		// the old stamp — equal means pointer-identical wherever pointers are
		// the fact, so memos recorded before this recompute and queries
		// computed after it keep agreeing.
		changedAt = m.changedAt
		value = m.value
	}
	db.memos[key] = &memo{value: value, verifiedAt: db.revision, changedAt: changedAt, deps: f.deps}
	return value
}

// equalValue is the early-cutoff equality, per query kind. Values carrying AST
// or definition pointers compare them by identity, never structurally: the
// pointer is the fact — a resolution binds a declaration object, a Named hangs
// type equality on its Def — so a structurally identical table built from a
// re-parsed file is a different fact and must propagate.
func equalValue(kind queryKind, old, fresh any) bool {
	switch kind {
	case qSymbols:
		return equalSymbolValues(old, fresh)
	case qResolve:
		return old == fresh // resolution is comparable
	case qFuncSymbols:
		return equalFuncSymbolValues(old, fresh)
	case qResolveFunc:
		return equalResolveFuncValues(old, fresh)
	case qImports:
		return equalImportValues(old, fresh)
	case qExports:
		return equalExportValues(old, fresh)
	case qReachable:
		a, _ := old.(map[FileID]bool)
		b, _ := fresh.(map[FileID]bool)
		return maps.Equal(a, b)
	case qModule:
		// A sink: no query depends on a module, so there is no propagation
		// to cut off, and comparing whole modules would cost more than it
		// could save.
		return false
	case qTypeDefs:
		return equalTypeDefValues(old, fresh)
	case qFuncs:
		return equalFuncShellValues(old, fresh)
	case qTypeOf:
		a, _ := old.(ir.Type)
		b, _ := fresh.(ir.Type)
		return equalTypes(a, b)
	case qValue:
		a, _ := old.(*ir.Constant)
		b, _ := fresh.(*ir.Constant)
		return equalConstants(a, b)
	default:
		return reflect.DeepEqual(old, fresh)
	}
}

// equalSymbolValues is the qSymbols arm of equalValue: the const declaration
// symbols compare by their pointer facts.
func equalSymbolValues(old, fresh any) bool {
	a, _ := old.(map[string]*ast.ConstDecl)
	b, _ := fresh.(map[string]*ast.ConstDecl)
	return maps.Equal(a, b)
}

// equalFuncSymbolValues is the qFuncSymbols arm of equalValue: the per-name
// overload sets compare by their declaration pointer facts.
func equalFuncSymbolValues(old, fresh any) bool {
	a, _ := old.(map[string][]*ast.FuncDecl)
	b, _ := fresh.(map[string][]*ast.FuncDecl)
	return maps.EqualFunc(a, b, slices.Equal)
}

// equalResolveFuncValues is the qResolveFunc arm of equalValue: the overload
// set's declaration pointers are the fact.
func equalResolveFuncValues(old, fresh any) bool {
	a, _ := old.([]*ast.FuncDecl)
	b, _ := fresh.([]*ast.FuncDecl)
	return slices.Equal(a, b)
}

// equalFuncShellValues is the qFuncs arm of equalValue: the function shells'
// pointers are the facts (an edit re-parses into fresh declarations, hence
// fresh shells).
func equalFuncShellValues(old, fresh any) bool {
	a, _ := old.([]*ir.Function)
	b, _ := fresh.([]*ir.Function)
	return slices.Equal(a, b)
}

// equalImportValues is the qImports arm of equalValue: the import table's
// value, type, function, and namespace bindings each compare by their facts.
func equalImportValues(old, fresh any) bool {
	a, _ := old.(importTable)
	b, _ := fresh.(importTable)
	return maps.Equal(a.values, b.values) && maps.Equal(a.types, b.types) &&
		maps.EqualFunc(a.funcs, b.funcs, equalFuncBindings) && maps.Equal(a.namespaces, b.namespaces)
}

// equalExportValues is the qExports arm of equalValue: the exported consts,
// types, and function overload sets each compare by their facts.
func equalExportValues(old, fresh any) bool {
	a, _ := old.(exports)
	b, _ := fresh.(exports)
	return maps.Equal(a.consts, b.consts) && maps.Equal(a.types, b.types) &&
		maps.EqualFunc(a.funcs, b.funcs, slices.Equal)
}

// equalTypeDefValues is the qTypeDefs arm of equalValue: the definition list,
// the by-name index, and the universe each compare by their facts.
func equalTypeDefValues(old, fresh any) bool {
	a, _ := old.(typeDefs)
	b, _ := fresh.(typeDefs)
	return slices.Equal(a.list, b.list) && maps.Equal(a.byName, b.byName) && maps.Equal(a.universe, b.universe)
}

// equalFuncBindings is funcBinding equality for the cutoff: the overload set's
// declaration pointers and the ambiguity verdict are the facts.
func equalFuncBindings(a, b funcBinding) bool {
	return a.ambiguous == b.ambiguous && slices.Equal(a.targets, b.targets)
}

// equalConstants is constant equality for the cutoff: structural for the data
// kinds, but a function value compares its literal by identity — the syntax
// pointer behind Constant.Fn, and like every other AST pointer in the engine
// the pointer is the fact, so a structurally identical literal from a
// re-parsed file must propagate rather than leave consumers holding a
// detached tree. It is
// ir.ConstantsEqual, the single shared definition the evaluator also folds on,
// so every ConstKind — enum, datetime, duration, record, error included — is
// covered; a kind added later without a case there panics rather than silently
// defeating the cutoff for that kind's whole dependency cone.
func equalConstants(a, b *ir.Constant) bool {
	return ir.ConstantsEqual(a, b)
}

// equalTypes is type equality for the cutoff: named types by their definition
// pointer, the rest structurally. Its sibling walker is types.sameType (the
// assignability comparison, which unwraps aliases where this one compares Def
// identity) — the two deliberately differ, and a new Type form must be taught
// to both.
func equalTypes(a, b ir.Type) bool {
	if a == b {
		return true
	}
	switch x := a.(type) {
	case *ir.Builtin:
		y, ok := b.(*ir.Builtin)
		return ok && x.Name == y.Name
	case *ir.Named:
		y, ok := b.(*ir.Named)
		return ok && x.Def == y.Def
	case *ir.App:
		return equalAppTypes(x, b)
	case *ir.Union:
		return equalUnionTypes(x, b)
	case *ir.Record:
		return equalRecordTypes(x, b)
	case *ir.Func:
		return equalFuncTypes(x, b)
	case *ir.TypeVar:
		y, ok := b.(*ir.TypeVar)
		return ok && x.Name == y.Name
	case *ir.SelfType:
		_, ok := b.(*ir.SelfType)
		return ok
	default:
		return false
	}
}

// equalAppTypes is the *ir.App arm of equalTypes: same definition pointer and
// element-wise equal arguments.
func equalAppTypes(x *ir.App, b ir.Type) bool {
	y, ok := b.(*ir.App)
	if !ok || x.Def != y.Def || len(x.Args) != len(y.Args) {
		return false
	}
	for i := range x.Args {
		if !equalTypes(x.Args[i], y.Args[i]) {
			return false
		}
	}
	return true
}

// equalUnionTypes is the *ir.Union arm of equalTypes: element-wise equal
// members in order.
func equalUnionTypes(x *ir.Union, b ir.Type) bool {
	y, ok := b.(*ir.Union)
	if !ok || len(x.Members) != len(y.Members) {
		return false
	}
	for i := range x.Members {
		if !equalTypes(x.Members[i], y.Members[i]) {
			return false
		}
	}
	return true
}

// equalRecordTypes is the *ir.Record arm of equalTypes: element-wise equal
// field names and types in order.
func equalRecordTypes(x *ir.Record, b ir.Type) bool {
	y, ok := b.(*ir.Record)
	if !ok || len(x.Fields) != len(y.Fields) {
		return false
	}
	for i := range x.Fields {
		if x.Fields[i].Name != y.Fields[i].Name || !equalTypes(x.Fields[i].Type, y.Fields[i].Type) {
			return false
		}
	}
	return true
}

// equalFuncTypes is the *ir.Func arm of equalTypes: equal result and
// element-wise equal parameters in order.
func equalFuncTypes(x *ir.Func, b ir.Type) bool {
	y, ok := b.(*ir.Func)
	if !ok || len(x.Params) != len(y.Params) || !equalTypes(x.Result, y.Result) {
		return false
	}
	for i := range x.Params {
		if !equalTypes(x.Params[i], y.Params[i]) {
			return false
		}
	}
	return true
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
		return db.computeResolve(key)
	case qFuncSymbols:
		in, _ := db.read(inputKey(key.file)).(fileInput)
		return buildFuncSymbols(in.file)
	case qResolveFunc:
		return db.computeResolveFunc(key)
	case qTypeOf:
		return infer.Decl(key.decl, typeEnv{q: engineQueries{db}, file: db.declFile[key.decl]})
	case qValue:
		return computeValue(db.declFile[key.decl], key.decl, engineQueries{db})
	case qTypeDefs:
		return db.computeTypeDefs(key.file)
	case qFuncs:
		return db.computeFuncs(key.file)
	case qExports:
		return db.computeExports(key.file)
	case qImports:
		return db.computeImports(key.file)
	case qReachable:
		// The walk reads each visited file's input through the engine, so
		// the set's dependencies are exactly the files it covers.
		return computeReachable(engineQueries{db}, key.file)
	case qModule:
		return db.computeModule(key)
	default:
		return nil
	}
}

// computeResolve is the qResolve arm of compute: locals shadow imports; an
// imported name claimed by two or more imports resolves to nothing and is
// flagged ambiguous.
func (db *database) computeResolve(key queryKey) any {
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
}

// computeResolveFunc is the qResolveFunc arm of compute: local functions shadow
// imports, exactly as values do.
func (db *database) computeResolveFunc(key queryKey) any {
	syms := db.read(funcSymbolsKey(key.file)).(map[string][]*ast.FuncDecl)
	if fds := syms[key.id.Name]; len(fds) > 0 {
		return fds
	}
	imp, _ := db.read(importsKey(key.file)).(importTable)
	if b, ok := imp.funcs[key.id.Name]; ok && !b.ambiguous {
		return b.targets
	}
	return ([]*ast.FuncDecl)(nil)
}

// computeModule is the qModule arm of compute. Every fact assemble pulls is
// read through the engine inside this computation, so the memo's dependencies
// are exactly what the module was built from; positions derive from the file's
// own tree, which the input read above covers.
func (db *database) computeModule(key queryKey) any {
	in, _ := db.read(inputKey(key.file)).(fileInput)
	if in.file == nil {
		return assembly{}
	}
	module, diags := assemble(key.file, in.file, positionsOf(cst.Root(in.file.Syntax())), engineQueries{db}, db.shells, db.fnShells)
	return assembly{module: module, diags: diags}
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
	case qFuncSymbols:
		return map[string][]*ast.FuncDecl{}
	case qResolveFunc:
		return ([]*ast.FuncDecl)(nil)
	case qTypeDefs:
		return typeDefs{}
	case qFuncs:
		return ([]*ir.Function)(nil)
	case qExports:
		return exports{}
	case qImports:
		return importTable{}
	case qReachable:
		return map[FileID]bool{} // unreachable: the walk reads only inputs
	case qModule:
		return assembly{} // unreachable: nothing assemble reads reads it back
	default:
		return nil
	}
}

// engineQueries is the memoizing implementation of the queries interface, used
// by Program; reads go through the database so dependencies are tracked.
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
	if r.ambiguous {
		return true
	}
	// A function name two or more imports claimed is just as ambiguous —
	// unless a local function shadows the imports.
	syms, _ := e.db.read(funcSymbolsKey(file)).(map[string][]*ast.FuncDecl)
	if len(syms[id.Name]) > 0 {
		return false
	}
	imp, _ := e.db.read(importsKey(file)).(importTable)
	b, ok := imp.funcs[id.Name]
	return ok && b.ambiguous
}

func (e engineQueries) resolveMember(file FileID, m *ast.MemberExpr) *ast.ConstDecl {
	return resolveMemberThrough(e, file, m)
}

func (e engineQueries) resolveFunc(file FileID, id *ast.Identifier) []*ast.FuncDecl {
	fds, _ := e.db.read(resolveFuncKey(file, id)).([]*ast.FuncDecl)
	return fds
}

func (e engineQueries) resolveFuncMember(file FileID, m *ast.MemberExpr) []*ast.FuncDecl {
	return resolveFuncMemberThrough(e, file, m)
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

func (e engineQueries) reachableFrom(file FileID) map[FileID]bool {
	m, _ := e.db.read(reachableKey(file)).(map[FileID]bool)
	return m
}

func (e engineQueries) moduleOf(file FileID) assembly {
	a, _ := e.db.read(moduleKey(file)).(assembly)
	return a
}

func (e engineQueries) preludeTypes() map[string]*ir.TypeDef { return e.db.prelude }

func (e engineQueries) registry() *builtin.Registry { return e.db.reg }

func (e engineQueries) funcsOf(file FileID) []*ir.Function {
	fns, _ := e.db.read(funcsKey(file)).([]*ir.Function)
	return fns
}

func (e engineQueries) constShellTable() map[*ast.ConstDecl]*ir.Const  { return e.db.shells }
func (e engineQueries) funcShellTable() map[*ast.FuncDecl]*ir.Function { return e.db.fnShells }
