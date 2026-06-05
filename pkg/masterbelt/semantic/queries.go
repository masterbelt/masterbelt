// This file holds the semantic query interface the assembler reads through and
// its direct (reference) implementation. The queries are the pure, memoizable
// facts an analysis needs — resolution, declaration types and values, the type
// universe, and the cross-file tables — expressed so both the full recompute
// (directQueries) and the incremental engine can serve them identically.
// typeEnv adapts the interface to package types/infer, and the symbol-table
// builders the queries memoize live here too.
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// queries are the pure, memoizable semantic facts the assembler needs.
type queries interface {
	// resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name. The file is the one the identifier
	// sits in — it decides the resolution scope. Keying resolution on the
	// identifier (not the whole symbol table) is what keeps early cutoff sharp:
	// a reference in an unedited declaration is a stable pointer, so editing an
	// unrelated constant does not invalidate it.
	resolve(file FileID, id *ast.Identifier) *ast.ConstDecl
	// ambiguousImport reports whether the identifier's name arrived from two or
	// more imports with distinct targets — harmless until used, then reported
	// at the reference.
	ambiguousImport(file FileID, id *ast.Identifier) bool
	// resolveMember returns the declaration a namespace member access
	// (geo.Origin) refers to, or nil when the receiver names no namespace or
	// the member is not among the target's exported values.
	resolveMember(file FileID, m *ast.MemberExpr) *ast.ConstDecl
	// resolveFunc returns the overload set a call's callee name refers to —
	// the file's own same-name declarations in source order, or the set an
	// unambiguous import binds — or nil if none has that name. Like resolve
	// it is keyed on the identifier, keeping early cutoff sharp.
	resolveFunc(file FileID, id *ast.Identifier) []*ast.FuncDecl
	// resolveFuncMember returns the overload set a namespace function call
	// (geo.area(...)) refers to: the target's exported functions of that
	// name, or nil.
	resolveFuncMember(file FileID, m *ast.MemberExpr) []*ast.FuncDecl
	// typeOf returns a constant's type (ir.Invalid when undeterminable).
	typeOf(decl *ast.ConstDecl) ir.Type
	// valueOf returns a constant's evaluated value, or nil when it cannot be
	// evaluated (missing initializer, undefined reference, cycle, type error,
	// or division by zero).
	valueOf(decl *ast.ConstDecl) *ir.Constant
	// typeDefs returns a file's resolved type definitions in source order —
	// the very objects Named types point at, shared by module assembly and
	// annotation resolution so type identity never forks.
	typeDefs(file FileID) []*ir.TypeDef
	// universe returns the named type definitions a file's type annotations
	// resolve in: its own declarations shadowing its imported ones.
	universe(file FileID) map[string]*ir.TypeDef
	// exportsOf returns a file's public surface.
	exportsOf(file FileID) exports
	// importsOf returns the bindings a file's use declarations introduce.
	importsOf(file FileID) importTable
	// usesOf returns where the project layer resolved a file's use paths; a
	// use absent from the table resolved to no file.
	usesOf(file FileID) map[*ast.UseDecl]FileID
	// reachableFrom returns the set of files the use graph reaches from file,
	// itself included — the fact behind cyclic_module: an import whose
	// target's reachable set contains the importer closes a cycle.
	reachableFrom(file FileID) map[FileID]bool
	// preludeTypes returns the implicit base tier of every file's universe:
	// the prelude barrel's exported types, beneath the file's imports — as if
	// each file began with `use * from "builtin.belt"`.
	preludeTypes() map[string]*ir.TypeDef
	// registry returns the builtin registry the analysis evaluates against —
	// the primitives' value ranges and the native implementations of their
	// operator methods. Their names resolve through preludeTypes, not here.
	registry() *builtin.Registry
}

// typeEnv adapts the semantic query interface to infer.Env, so the type
// inference and checking in package types/infer can read resolution, declaration
// types, and the builtin registry through the same memoizing engine. It carries
// the file whose scope identifiers resolve in, so infer.Env stays file-blind.
type typeEnv struct {
	q    queries
	file FileID
}

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(e.file, id) }
func (e typeEnv) ResolveMember(m *ast.MemberExpr) *ast.ConstDecl {
	return e.q.resolveMember(e.file, m)
}
func (e typeEnv) ResolveFunc(id *ast.Identifier) []*ast.FuncDecl {
	return e.q.resolveFunc(e.file, id)
}
func (e typeEnv) ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl {
	return e.q.resolveFuncMember(e.file, m)
}
func (e typeEnv) TypeOf(decl *ast.ConstDecl) ir.Type { return e.q.typeOf(decl) }
func (e typeEnv) Universe() map[string]*ir.TypeDef   { return e.q.universe(e.file) }
func (e typeEnv) QualifiedType(namespace, name string) *ir.TypeDef {
	return qualifiedFrom(e.q, e.q.importsOf(e.file))(namespace, name)
}
func (e typeEnv) Registry() *builtin.Registry { return e.q.registry() }

// buildSymbols maps each declared name to its first declaration. A nil file
// (an input never set) has no symbols.
func buildSymbols(file *ast.File) map[string]*ast.ConstDecl {
	syms := map[string]*ast.ConstDecl{}
	if file == nil {
		return syms
	}
	for _, decl := range file.Decls {
		if decl.Name != "" {
			if _, exists := syms[decl.Name]; !exists {
				syms[decl.Name] = decl
			}
		}
	}
	return syms
}

// buildFuncSymbols maps each declared function name to its overload set: every
// same-name declaration, in source order. A nil file (an input never set) has
// no functions.
func buildFuncSymbols(file *ast.File) map[string][]*ast.FuncDecl {
	syms := map[string][]*ast.FuncDecl{}
	if file == nil {
		return syms
	}
	for _, fd := range file.Funcs {
		if fd.Name != "" {
			syms[fd.Name] = append(syms[fd.Name], fd)
		}
	}
	return syms
}

// --- direct (reference) query implementation --------------------------------

// directQueries computes the semantic facts directly, memoizing within a
// single analysis and guarding against cycles — both the per-declaration kind
// (an un-annotated reference chain) and the per-file kind (a module cycle
// re-entering its own tables, mirroring the engine's running guard). It
// carries no state across analyses — that is the incremental engine's job.
type directQueries struct {
	files    map[FileID]*ast.File
	uses     map[FileID]map[*ast.UseDecl]FileID
	declFile map[*ast.ConstDecl]FileID
	reg      *builtin.Registry
	prelude  map[string]*ir.TypeDef
	fnShells map[*ast.FuncDecl]*ir.Function

	syms   map[FileID]map[string]*ast.ConstDecl
	fnSyms map[FileID]map[string][]*ast.FuncDecl
	imps   map[FileID]importTable
	exps   map[FileID]exports
	defs   map[FileID]typeDefs

	importing map[FileID]bool
	exporting map[FileID]bool
	resolving map[FileID]bool

	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool
	valueMemo map[*ast.ConstDecl]*ir.Constant
	valuing   map[*ast.ConstDecl]bool

	reach map[FileID]map[FileID]bool
}

func newDirectQueries(files map[FileID]*ast.File, uses map[FileID]map[*ast.UseDecl]FileID, u builtins) *directQueries {
	d := &directQueries{
		files:     files,
		uses:      uses,
		declFile:  map[*ast.ConstDecl]FileID{},
		reg:       u.reg,
		prelude:   u.prelude,
		fnShells:  funcShells(files),
		syms:      map[FileID]map[string]*ast.ConstDecl{},
		fnSyms:    map[FileID]map[string][]*ast.FuncDecl{},
		imps:      map[FileID]importTable{},
		exps:      map[FileID]exports{},
		defs:      map[FileID]typeDefs{},
		importing: map[FileID]bool{},
		exporting: map[FileID]bool{},
		resolving: map[FileID]bool{},
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		valueMemo: map[*ast.ConstDecl]*ir.Constant{},
		valuing:   map[*ast.ConstDecl]bool{},
		reach:     map[FileID]map[FileID]bool{},
	}
	for id, f := range files {
		if f == nil {
			continue
		}
		for _, decl := range f.Decls {
			d.declFile[decl] = id
		}
	}
	return d
}

// memoize returns the cached value for key, computing and caching it on the
// first demand. A re-entered computation (a cycle) yields zero without
// caching — the same fallback the engine's running guard gives the
// corresponding query, so the two implementations cannot diverge on cycles.
func memoize[K comparable, V any](memo map[K]V, running map[K]bool, key K, zero V, compute func() V) V {
	if v, ok := memo[key]; ok {
		return v
	}
	if running[key] {
		return zero
	}
	running[key] = true
	v := compute()
	delete(running, key)
	memo[key] = v
	return v
}

func (d *directQueries) preludeTypes() map[string]*ir.TypeDef { return d.prelude }

func (d *directQueries) registry() *builtin.Registry { return d.reg }

func (d *directQueries) symbols(f FileID) map[string]*ast.ConstDecl {
	if s, ok := d.syms[f]; ok {
		return s
	}
	s := buildSymbols(d.files[f])
	d.syms[f] = s
	return s
}

func (d *directQueries) resolve(f FileID, id *ast.Identifier) *ast.ConstDecl {
	if decl := d.symbols(f)[id.Name]; decl != nil {
		return decl
	}
	if b, ok := d.importsOf(f).values[id.Name]; ok && !b.ambiguous {
		return b.target
	}
	return nil
}

func (d *directQueries) funcSymbols(f FileID) map[string][]*ast.FuncDecl {
	if s, ok := d.fnSyms[f]; ok {
		return s
	}
	s := buildFuncSymbols(d.files[f])
	d.fnSyms[f] = s
	return s
}

func (d *directQueries) resolveFunc(f FileID, id *ast.Identifier) []*ast.FuncDecl {
	if fds := d.funcSymbols(f)[id.Name]; len(fds) > 0 {
		return fds
	}
	if b, ok := d.importsOf(f).funcs[id.Name]; ok && !b.ambiguous {
		return b.targets
	}
	return nil
}

func (d *directQueries) resolveFuncMember(f FileID, m *ast.MemberExpr) []*ast.FuncDecl {
	return resolveFuncMemberThrough(d, f, m)
}

func (d *directQueries) ambiguousImport(f FileID, id *ast.Identifier) bool {
	if d.symbols(f)[id.Name] != nil {
		return false
	}
	if b, ok := d.importsOf(f).values[id.Name]; ok && b.ambiguous {
		return true
	}
	if len(d.funcSymbols(f)[id.Name]) > 0 {
		return false
	}
	b, ok := d.importsOf(f).funcs[id.Name]
	return ok && b.ambiguous
}

func (d *directQueries) resolveMember(f FileID, m *ast.MemberExpr) *ast.ConstDecl {
	return resolveMemberThrough(d, f, m)
}

func (d *directQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	return memoize(d.typeMemo, d.typing, decl, ir.Invalid, func() ir.Type {
		return infer.Decl(decl, typeEnv{q: d, file: d.declFile[decl]})
	})
}

func (d *directQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	return memoize(d.valueMemo, d.valuing, decl, nil, func() *ir.Constant {
		return computeValue(d.declFile[decl], decl, d)
	})
}

func (d *directQueries) importsOf(f FileID) importTable {
	return memoize(d.imps, d.importing, f, importTable{}, func() importTable {
		return buildImports(d, d.files[f], d.uses[f])
	})
}

func (d *directQueries) exportsOf(f FileID) exports {
	return memoize(d.exps, d.exporting, f, exports{}, func() exports {
		return buildExports(d, d.files[f], d.uses[f], d.typeDefsOf(f).byName)
	})
}

func (d *directQueries) typeDefsOf(f FileID) typeDefs {
	return memoize(d.defs, d.resolving, f, typeDefs{}, func() typeDefs {
		imp := d.importsOf(f)
		fns := bodyFuncs{local: funcShellsByName(d.files[f], d.fnShells), qualified: qualifiedFuncsFrom(d, imp), shells: d.fnShells}
		return buildTypeDefs(d, d.files[f], imp, fns)
	})
}

func (d *directQueries) typeDefs(f FileID) []*ir.TypeDef { return d.typeDefsOf(f).list }

func (d *directQueries) universe(f FileID) map[string]*ir.TypeDef { return d.typeDefsOf(f).universe }

func (d *directQueries) usesOf(f FileID) map[*ast.UseDecl]FileID { return d.uses[f] }

func (d *directQueries) reachableFrom(f FileID) map[FileID]bool {
	if r, ok := d.reach[f]; ok {
		return r
	}
	r := computeReachable(d, f)
	d.reach[f] = r
	return r
}
