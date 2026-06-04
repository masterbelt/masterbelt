package semantic

import (
	"maps"

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

// exports is the value of an exports query: a file's public surface, values
// and types by name. Per name the file's own pub declaration wins; pub use
// re-exports merge behind it in source order, first claim winning.
type exports struct {
	consts map[string]*ast.ConstDecl
	types  map[string]*ir.TypeDef
}

// binding is one imported name: its target, and whether two or more imports
// claimed the name with distinct targets (the same target arriving twice — a
// selective import overlapping a wildcard, say — is not a conflict).
type binding[T comparable] struct {
	target    T
	ambiguous bool
}

// importTable is the value of an imports query: the names a file's use
// declarations bind locally. Selective and wildcard imports land in
// values/types; namespace imports in namespaces (first declaration of a
// namespace name wins).
type importTable struct {
	values     map[string]binding[*ast.ConstDecl]
	types      map[string]binding[*ir.TypeDef]
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

// computeImports builds a file's import table from its use declarations and
// the exports of their targets.
func (db *database) computeImports(f FileID) importTable {
	in, _ := db.read(inputKey(f)).(fileInput)
	t := importTable{
		values:     map[string]binding[*ast.ConstDecl]{},
		types:      map[string]binding[*ir.TypeDef]{},
		namespaces: map[string]FileID{},
	}
	if in.file == nil {
		return t
	}
	for _, u := range in.file.Uses {
		target, ok := in.uses[u]
		if !ok {
			continue // unresolved: assemble reports use_not_found at the decl
		}
		switch {
		case u.Namespace != "":
			if _, exists := t.namespaces[u.Namespace]; !exists {
				t.namespaces[u.Namespace] = target
			}
		case u.Star:
			exp, _ := db.read(exportsKey(target)).(exports)
			for name, decl := range exp.consts {
				addBinding(t.values, name, decl)
			}
			for name, def := range exp.types {
				addBinding(t.types, name, def)
			}
		default:
			exp, _ := db.read(exportsKey(target)).(exports)
			for _, name := range u.Names {
				if decl, ok := exp.consts[name]; ok {
					addBinding(t.values, name, decl)
				}
				if def, ok := exp.types[name]; ok {
					addBinding(t.types, name, def)
				}
				// A name in neither table: assemble reports not_exported.
			}
		}
	}
	return t
}

// computeExports builds a file's public surface: its own pub declarations,
// then its pub use re-exports in source order. A pub namespace import
// re-exports nothing — a namespace binding is file-local.
func (db *database) computeExports(f FileID) exports {
	out := exports{consts: map[string]*ast.ConstDecl{}, types: map[string]*ir.TypeDef{}}
	in, _ := db.read(inputKey(f)).(fileInput)
	if in.file == nil {
		return out
	}

	for _, decl := range in.file.Decls {
		if decl.Public && decl.Name != "" {
			if _, ok := out.consts[decl.Name]; !ok {
				out.consts[decl.Name] = decl
			}
		}
	}
	defs, _ := db.read(typeDefsKey(f)).(typeDefs)
	for name, def := range defs.byName {
		if def.Public {
			out.types[name] = def
		}
	}

	for _, u := range in.file.Uses {
		if !u.Public || u.Namespace != "" {
			continue
		}
		target, ok := in.uses[u]
		if !ok {
			continue
		}
		exp, _ := db.read(exportsKey(target)).(exports)
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
		}
		if u.Star {
			// A barrel: the target's whole surface re-exports. Iterating two
			// maps is order-free because reexport is first-claim per name and
			// a name cannot collide with itself within one target.
			for name := range exp.consts {
				reexport(name)
			}
			for name := range exp.types {
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

// computeTypeDefs resolves a file's type declarations with its imported type
// names in scope (its own declarations shadow them), and assembles the
// annotation universe from the same definitions.
func (db *database) computeTypeDefs(f FileID) typeDefs {
	td := typeDefs{byName: map[string]*ir.TypeDef{}, universe: map[string]*ir.TypeDef{}}
	in, _ := db.read(inputKey(f)).(fileInput)
	if in.file == nil {
		return td
	}

	imp, _ := db.read(importsKey(f)).(importTable)
	extern := make(map[string]*ir.TypeDef, len(imp.types))
	for name, b := range imp.types {
		if !b.ambiguous {
			extern[name] = b.target
		}
	}

	td.list = resolveTypes(in.file, db.reg, nil, nil, extern)
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
