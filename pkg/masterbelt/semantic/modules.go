package semantic

import (
	"maps"
	"slices"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// This file holds the cross-file resolution queries: what a file exports, what
// its use declarations bind, and the type universe its annotations resolve in.
// Use paths were already resolved to FileIDs by the project layer and arrive
// as part of each file's input; everything here is pure table-building over
// those inputs, so the engine can memoize it per file.

// resolution is the value of a resolve query: the declaration an identifier
// refers to, or — when the name arrived from two or more imports with distinct
// targets — an ambiguity, which is harmless until the name is actually used
// and reported there.
type resolution struct {
	decl      *ast.ConstDecl
	ambiguous bool
}

// typeDefs is the value of a typeDefs query: one file's resolved type
// definitions, in source order and by name, plus the universe its type
// annotations resolve in — its own definitions shadowing its imported ones.
// These definitions are the identity Named types compare by, so every reader
// (module assembly, annotation resolution, exports) must share them.
type typeDefs struct {
	list     []*ir.TypeDef
	byName   map[string]*ir.TypeDef
	universe map[string]*ir.TypeDef
}

// exports is the value of an exports query: a file's public surface — values,
// types, and functions by name (a function name carrying its whole public
// overload set). Per name the file's own pub declaration wins; pub use
// re-exports merge behind it in source order, first claim winning.
type exports struct {
	consts map[string]*ast.ConstDecl
	types  map[string]*ir.TypeDef
	funcs  map[string][]*ast.FuncDecl
}

// binding is one imported name: its target, and whether two or more imports
// claimed the name with distinct targets (the same target arriving twice — a
// selective import overlapping a wildcard, say — is not a conflict).
type binding[T comparable] struct {
	target    T
	ambiguous bool
}

// funcBinding is one imported function name: its overload set, and whether
// two or more imports claimed the name with distinct sets (the same set
// arriving twice is not a conflict).
type funcBinding struct {
	targets   []*ast.FuncDecl
	ambiguous bool
}

// importTable is the value of an imports query: the names a file's use
// declarations bind locally. Selective and wildcard imports land in
// values/types/funcs; namespace imports in namespaces (first declaration of a
// namespace name wins).
type importTable struct {
	values     map[string]binding[*ast.ConstDecl]
	types      map[string]binding[*ir.TypeDef]
	funcs      map[string]funcBinding
	namespaces map[string]FileID
}

// addBinding records name -> target, marking the name ambiguous when a
// different target already claimed it.
func addBinding[T comparable](m map[string]binding[T], name string, target T) {
	if b, ok := m[name]; ok {
		if b.target != target {
			b.ambiguous = true
			m[name] = b
		}
		return
	}
	m[name] = binding[T]{target: target}
}

// addFuncBinding is addBinding for overload sets: a different set claiming
// the same name marks it ambiguous.
func addFuncBinding(m map[string]funcBinding, name string, targets []*ast.FuncDecl) {
	if b, ok := m[name]; ok {
		if !slices.Equal(b.targets, targets) {
			b.ambiguous = true
			m[name] = b
		}
		return
	}
	m[name] = funcBinding{targets: targets}
}

// qualifiedFrom builds the namespace-qualified type lookup (geo.Point) over an
// import table: the qualifier must be one of its namespace bindings and the
// name among the target's exported types. Reads go through q, so the engine
// tracks the target's exports as a dependency of whatever resolves the name.
func qualifiedFrom(q queries, imp importTable) func(namespace, name string) *ir.TypeDef {
	return func(namespace, name string) *ir.TypeDef {
		target, ok := imp.namespaces[namespace]
		if !ok {
			return nil
		}
		return q.exportsOf(target).types[name]
	}
}

// resolveMemberThrough resolves a namespace member access (geo.Origin) through
// q: the receiver must name a namespace import of file — and resolve to no
// value, since locals and imported values shadow namespaces — and the member
// must be among the target's exported values.
func resolveMemberThrough(q queries, file FileID, m *ast.MemberExpr) *ast.ConstDecl {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil
	}
	if q.resolve(file, recv) != nil {
		return nil // a value binding shadows the namespace
	}
	target, ok := q.importsOf(file).namespaces[recv.Name]
	if !ok {
		return nil
	}
	return q.exportsOf(target).consts[m.Member.Name]
}

// resolveFuncMemberThrough is resolveMemberThrough for a namespace function
// call (geo.area(...)): the member resolves to the target's exported overload
// set of that name.
func resolveFuncMemberThrough(q queries, file FileID, m *ast.MemberExpr) []*ast.FuncDecl {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return nil
	}
	if q.resolve(file, recv) != nil {
		return nil // a value binding shadows the namespace
	}
	target, ok := q.importsOf(file).namespaces[recv.Name]
	if !ok {
		return nil
	}
	return q.exportsOf(target).funcs[m.Member.Name]
}

// qualifiedFuncsFrom builds the namespace-qualified function lookup over an
// import table, for the body scopes (where no value bindings can shadow a
// namespace). Reads go through q, so the engine tracks the target's exports
// as a dependency.
func qualifiedFuncsFrom(q queries, imp importTable) func(namespace, name string) []*ast.FuncDecl {
	return func(namespace, name string) []*ast.FuncDecl {
		target, ok := imp.namespaces[namespace]
		if !ok {
			return nil
		}
		return q.exportsOf(target).funcs[name]
	}
}

// buildImports builds a file's import table from its use declarations and the
// exports of their targets, read through q so both query implementations (the
// engine, which tracks the reads as dependencies, and the direct oracle) share
// one rule.
func buildImports(q queries, file *ast.File, uses map[*ast.UseDecl]FileID) importTable {
	t := importTable{
		values:     map[string]binding[*ast.ConstDecl]{},
		types:      map[string]binding[*ir.TypeDef]{},
		funcs:      map[string]funcBinding{},
		namespaces: map[string]FileID{},
	}
	if file == nil {
		return t
	}
	for _, u := range file.Uses {
		target, ok := uses[u]
		if !ok {
			continue // unresolved: assemble reports use_not_found at the decl
		}
		switch {
		case u.Namespace != "":
			if _, exists := t.namespaces[u.Namespace]; !exists {
				t.namespaces[u.Namespace] = target
			}
		case u.Star:
			exp := q.exportsOf(target)
			for name, decl := range exp.consts {
				addBinding(t.values, name, decl)
			}
			for name, def := range exp.types {
				addBinding(t.types, name, def)
			}
			for name, fds := range exp.funcs {
				addFuncBinding(t.funcs, name, fds)
			}
		default:
			exp := q.exportsOf(target)
			for _, name := range u.Names {
				if decl, ok := exp.consts[name]; ok {
					addBinding(t.values, name, decl)
				}
				if def, ok := exp.types[name]; ok {
					addBinding(t.types, name, def)
				}
				if fds, ok := exp.funcs[name]; ok {
					addFuncBinding(t.funcs, name, fds)
				}
				// A name in no table: assemble reports not_exported.
			}
		}
	}
	return t
}

// buildExports builds a file's public surface: its own pub declarations (with
// ownTypes its resolved public type definitions), then its pub use re-exports
// in source order. A pub namespace import re-exports nothing — a namespace
// binding is file-local.
func buildExports(q queries, file *ast.File, uses map[*ast.UseDecl]FileID, ownTypes map[string]*ir.TypeDef) exports {
	out := exports{consts: map[string]*ast.ConstDecl{}, types: map[string]*ir.TypeDef{}, funcs: map[string][]*ast.FuncDecl{}}
	if file == nil {
		return out
	}

	for _, decl := range file.Decls {
		if decl.Public && decl.Name != "" {
			if _, ok := out.consts[decl.Name]; !ok {
				out.consts[decl.Name] = decl
			}
		}
	}
	for name, def := range ownTypes {
		if def.Public {
			out.types[name] = def
		}
	}
	// A function name exports its whole public overload set, in source order.
	for _, fd := range file.Funcs {
		if fd.Public && fd.Name != "" {
			out.funcs[fd.Name] = append(out.funcs[fd.Name], fd)
		}
	}

	for _, u := range file.Uses {
		if !u.Public || u.Namespace != "" {
			continue
		}
		target, ok := uses[u]
		if !ok {
			continue
		}
		exp := q.exportsOf(target)
		reexport := func(name string) {
			if decl, ok := exp.consts[name]; ok {
				if _, taken := out.consts[name]; !taken {
					out.consts[name] = decl
				}
			}
			if def, ok := exp.types[name]; ok {
				if _, taken := out.types[name]; !taken {
					out.types[name] = def
				}
			}
			if fds, ok := exp.funcs[name]; ok {
				if _, taken := out.funcs[name]; !taken {
					out.funcs[name] = fds
				}
			}
		}
		if u.Star {
			// A barrel: the target's whole surface re-exports. Iterating the
			// maps is order-free because reexport is first-claim per name and
			// a name cannot collide with itself within one target.
			for name := range exp.consts {
				reexport(name)
			}
			for name := range exp.types {
				reexport(name)
			}
			for name := range exp.funcs {
				reexport(name)
			}
		} else {
			for _, name := range u.Names {
				reexport(name)
			}
		}
	}
	return out
}

// buildTypeDefs resolves a file's type declarations with its imported type
// names and the prelude surface in scope (its own declarations shadow both)
// and its namespace-qualified names resolvable through q, and assembles the
// annotation universe from the same definitions. imp must be the file's
// import table, and fns its function shells by name (so method bodies lower
// calls of top-level functions).
func buildTypeDefs(q queries, fileID FileID, file *ast.File, imp importTable, fns bodyFuncs) typeDefs {
	td := typeDefs{byName: map[string]*ir.TypeDef{}, universe: map[string]*ir.TypeDef{}}
	if file == nil {
		return td
	}
	extern := outerTypes(q, imp)
	// Enum member initializers fold through the file's evaluator (which the
	// memoizing engine tracks and cycle-guards); a const reference in an enum
	// value resolves like any other.
	td.list = resolveTypes(evalEnv{q: q, file: fileID}, file, nil, nil, q.registry(), extern, qualifiedFrom(q, imp), fns)
	for _, def := range td.list {
		if def.Name != "" {
			if _, ok := td.byName[def.Name]; !ok {
				td.byName[def.Name] = def
			}
		}
	}
	maps.Copy(td.universe, extern)
	maps.Copy(td.universe, td.byName) // own definitions shadow imports
	return td
}

// outerTypes is everything a file's type names resolve to beneath its own
// declarations: the prelude surface with the import table's unambiguous type
// bindings over it. Imports shadow the prelude; the file's own declarations
// (layered on by the callers) shadow both.
func outerTypes(q queries, imp importTable) map[string]*ir.TypeDef {
	out := make(map[string]*ir.TypeDef, len(q.preludeTypes())+len(imp.types))
	maps.Copy(out, q.preludeTypes())
	for name, b := range imp.types {
		if !b.ambiguous {
			out[name] = b.target
		}
	}
	return out
}

// computeImports, computeExports, and computeTypeDefs are the engine-side
// query functions: they read the inputs (and each other) through the database
// so every read is captured as a dependency, then delegate to the shared
// builders.
func (db *database) computeImports(f FileID) importTable {
	in, _ := db.read(inputKey(f)).(fileInput)
	return buildImports(engineQueries{db}, in.file, in.uses)
}

func (db *database) computeExports(f FileID) exports {
	in, _ := db.read(inputKey(f)).(fileInput)
	defs, _ := db.read(typeDefsKey(f)).(typeDefs)
	return buildExports(engineQueries{db}, in.file, in.uses, defs.byName)
}

func (db *database) computeTypeDefs(f FileID) typeDefs {
	in, _ := db.read(inputKey(f)).(fileInput)
	imp, _ := db.read(importsKey(f)).(importTable)
	q := engineQueries{db}
	fns := bodyFuncs{local: funcShellsByName(in.file, db.fnShells), qualified: qualifiedFuncsFrom(q, imp), shells: db.fnShells}
	return buildTypeDefs(q, f, in.file, imp, fns)
}
