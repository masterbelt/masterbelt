// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// Operators have already been desugared to method calls by the AST layer, so
// 1 + 2 arrives as 1.add(2). Typing and evaluation are therefore uniform: every
// expression is a literal, a value reference, or a method call, and a call's
// type comes from the method's signature (package types) while its value comes
// from the method's native implementation (the builtin registry's intrinsics).
//
// The semantic facts a program needs — the symbol table, each constant's type,
// and each constant's evaluated value — are expressed as a small set of pure
// queries (the queries interface). assemble turns those queries plus the AST
// into the IR and diagnostics. Two query implementations share that one
// assembler: a direct one (this file), used by Analyze for a full recompute and
// as the oracle, and an incremental, memoizing one backed by the query database
// (engine.go), used by Document. Because both feed the same assembler, the
// incremental result is identical to the full one.
//
// The package is split by concern: this file holds the query interface and the
// assembler; eval.go folds constants; lower.go binds names for the AST-to-IR
// walk (package lower); resolve.go resolves type declarations; check.go runs the
// expression and method-body diagnostics; positions.go anchors diagnostics to
// source; engine.go and document.go are the incremental façade.
package semantic

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ast"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
)

// queries are the pure, memoizable semantic facts the assembler needs.
type queries interface {
	// resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name. Keying resolution on the identifier
	// (not the whole symbol table) is what keeps early cutoff sharp: a reference
	// in an unedited declaration is a stable pointer, so editing an unrelated
	// constant does not invalidate it.
	resolve(id *ast.Identifier) *ast.ConstDecl
	// typeOf returns a constant's type (ir.Invalid when undeterminable).
	typeOf(decl *ast.ConstDecl) ir.Type
	// valueOf returns a constant's evaluated value, or nil when it cannot be
	// evaluated (missing initializer, undefined reference, cycle, type error,
	// or division by zero).
	valueOf(decl *ast.ConstDecl) *ir.Constant
	// registry returns the builtin registry the analysis types and evaluates
	// against — the source of primitive types, their value ranges, and the
	// native implementations of their operator methods.
	registry() *builtin.Registry
}

// typeEnv adapts the semantic query interface to infer.Env, so the type
// inference and checking in package types/infer can read resolution, declaration
// types, and the builtin registry through the same memoizing engine.
type typeEnv struct{ q queries }

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(id) }
func (e typeEnv) TypeOf(decl *ast.ConstDecl) ir.Type        { return e.q.typeOf(decl) }
func (e typeEnv) Registry() *builtin.Registry               { return e.q.registry() }

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// reference analysis and the oracle for the incremental Document.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	return assemble(file, positionsOf(doc.Concrete().Tree()), newDirectQueries(file, universe()))
}

// assemble builds the IR module and all semantic diagnostics from the AST, using
// q for the resolution and typing facts. It is shared by the reference and
// incremental analyzers, so they cannot diverge.
func assemble(file *ast.File, positions map[cst.Green]span, q queries) (*ir.Module, []diagnostic.Diagnostic) {
	diags := &diagnostic.List{}
	at := func(n ast.Node) span { return spanOf(positions, n) }
	env := typeEnv{q}
	reg := q.registry()

	// Create the IR constants first so references can bind to them.
	module := &ir.Module{}
	irOf := make(map[*ast.ConstDecl]*ir.Const, len(file.Decls))
	for _, decl := range file.Decls {
		c := &ir.Const{Name: decl.Name, Public: decl.Public, Doc: decl.Doc, Syntax: decl}
		irOf[decl] = c
		module.Consts = append(module.Consts, c)
	}

	// Redeclarations of the same name.
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		if decl.Name == "" {
			continue // already a parse diagnostic
		}
		if seen[decl.Name] {
			s := at(decl)
			diags.Add(newDuplicateDeclarationDiagnostic(s.offset, s.width, decl.Name))
		}
		seen[decl.Name] = true
	}

	cyclic := cyclicDecls(file, q)

	for _, decl := range file.Decls {
		c := irOf[decl]
		c.Value = lower.Value(decl.Value, constBinder{q: q, irOf: irOf})
		c.Type = q.typeOf(decl)
		c.Eval = q.valueOf(decl)

		// Undefined references: every value-position identifier that resolves to
		// no declaration (method names are not value references, so walkIdents
		// skips them).
		if decl.Value != nil {
			ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
				if id.Name != "" && q.resolve(id) == nil {
					s := at(id)
					diags.Add(newUndefinedNameDiagnostic(s.offset, s.width, id.Name))
				}
			})
			// Operator type errors: the innermost method call whose operand
			// types it is not defined on.
			infer.Check(decl.Value, env, func(node ast.Node, method, operands string) {
				s := at(node)
				diags.Add(newInvalidOperationDiagnostic(s.offset, s.width, method, operands))
			})
			// Division or remainder by a zero divisor.
			checkDivByZero(decl.Value, q, func(node ast.Node) {
				s := at(node)
				diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
			})
		}

		if decl.Type != nil {
			// Resolve the annotation with reporting enabled, so an unknown type
			// name anywhere in it (e.g. list<Bogus>) is diagnosed at its own node.
			// A file's own type declarations are not visible to a const annotation,
			// so the resolver is given no file defs.
			r := &infer.TypeResolver{Reg: reg, Report: unknownTypeReporter(at, diags)}
			annType := r.ResolveType(decl.Type, nil)
			// A collection literal is checked element-wise below (collectionCheck),
			// which reports precisely; any other value's inferred type must be
			// compatible with the annotation here.
			_, isColl := decl.Value.(*ast.CollectionLit)
			if annType != ir.Invalid && decl.Value != nil && !isColl {
				if exprT := infer.Expr(decl.Value, env); exprT != ir.Invalid && !types.Compatible(reg, annType, exprT) {
					s := at(decl.Value)
					diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, exprT.String(), annType.String()))
				}
			}
		}
		if cyclic[decl] {
			s := at(decl)
			diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		}
		// An integer value outside its concrete type's range overflows. The
		// arbitrary-precision int has no fixed range (Fits accepts any value),
		// and booleans never overflow.
		if c.Eval != nil && c.Eval.Kind == ir.ConstInt && !types.Fits(reg, c.Type, c.Eval.Int) {
			s := at(decl.Value)
			diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, c.Eval.String(), c.Type.String()))
		}
		// A collection literal is type-checked element-wise against its type
		// (the annotation, or the inferred element type), with each element's
		// value range checked too. An empty or heterogeneous literal with no
		// annotation cannot be inferred and is reported.
		if lit, ok := decl.Value.(*ast.CollectionLit); ok {
			cc := collectionChecker{env: env, q: q, reg: reg, at: at, diags: diags}
			cc.check(lit, decl.Type != nil, c.Type)
		}
	}

	// Resolve the file's type declarations into the module's type definitions,
	// then type-check each method body against its declared result type.
	module.Types = resolveTypes(file, reg, at, diags)
	checkMethodBodies(file, reg, module.Types, func(node ast.Node, got, want ir.Type) {
		s := at(node)
		diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
	})

	items := diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

// buildSymbols maps each declared name to its first declaration.
func buildSymbols(file *ast.File) map[string]*ast.ConstDecl {
	syms := map[string]*ast.ConstDecl{}
	for _, decl := range file.Decls {
		if decl.Name != "" {
			if _, exists := syms[decl.Name]; !exists {
				syms[decl.Name] = decl
			}
		}
	}
	return syms
}

// cyclicDecls returns the declarations caught in a type-inference cycle. A
// declaration's type depends on the types of the value references in its
// initializer, unless an annotation fixes it; the result is a general directed
// graph (an expression may reference several names), so its cycles are found
// with a coloured depth-first search.
func cyclicDecls(file *ast.File, q queries) map[*ast.ConstDecl]bool {
	deps := func(decl *ast.ConstDecl) []*ast.ConstDecl {
		if decl.Type != nil || decl.Value == nil {
			return nil // an annotation breaks the inheritance chain
		}
		var out []*ast.ConstDecl
		ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
			if t := q.resolve(id); t != nil {
				out = append(out, t)
			}
		})
		return out
	}

	const (
		white = iota
		gray
		black
	)
	color := map[*ast.ConstDecl]int{}
	cyclic := map[*ast.ConstDecl]bool{}
	var stack []*ast.ConstDecl

	var dfs func(decl *ast.ConstDecl)
	dfs = func(decl *ast.ConstDecl) {
		color[decl] = gray
		stack = append(stack, decl)
		for _, dep := range deps(decl) {
			switch color[dep] {
			case white:
				dfs(dep)
			case gray:
				// Back edge: everything from dep to the top of the stack is on
				// the cycle.
				for i := len(stack) - 1; i >= 0; i-- {
					cyclic[stack[i]] = true
					if stack[i] == dep {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[decl] = black
	}

	for _, decl := range file.Decls {
		if color[decl] == white {
			dfs(decl)
		}
	}
	return cyclic
}

// --- direct (reference) query implementation --------------------------------

// directQueries computes the semantic facts directly, memoizing types and values
// within a single analysis (and guarding against cycles). It carries no state
// across analyses — that is the incremental engine's job.
type directQueries struct {
	file      *ast.File
	reg       *builtin.Registry
	syms      map[string]*ast.ConstDecl
	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool
	valueMemo map[*ast.ConstDecl]*ir.Constant
	valuing   map[*ast.ConstDecl]bool
}

func newDirectQueries(file *ast.File, reg *builtin.Registry) *directQueries {
	return &directQueries{
		file:      file,
		reg:       reg,
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		valueMemo: map[*ast.ConstDecl]*ir.Constant{},
		valuing:   map[*ast.ConstDecl]bool{},
	}
}

func (d *directQueries) registry() *builtin.Registry { return d.reg }

func (d *directQueries) symbols() map[string]*ast.ConstDecl {
	if d.syms == nil {
		d.syms = buildSymbols(d.file)
	}
	return d.syms
}

func (d *directQueries) resolve(id *ast.Identifier) *ast.ConstDecl {
	return d.symbols()[id.Name]
}

func (d *directQueries) typeOf(decl *ast.ConstDecl) ir.Type {
	if t, done := d.typeMemo[decl]; done {
		return t
	}
	if d.typing[decl] {
		return ir.Invalid // cycle
	}
	d.typing[decl] = true
	t := infer.Decl(decl, typeEnv{d})
	d.typing[decl] = false
	d.typeMemo[decl] = t
	return t
}

func (d *directQueries) valueOf(decl *ast.ConstDecl) *ir.Constant {
	if v, done := d.valueMemo[decl]; done {
		return v
	}
	if d.valuing[decl] {
		return nil // cycle
	}
	d.valuing[decl] = true
	v := computeValue(decl, d)
	d.valuing[decl] = false
	d.valueMemo[decl] = v
	return v
}
