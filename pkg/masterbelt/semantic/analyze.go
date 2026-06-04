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

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/eval"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lower"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
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
// types, and the builtin registry through the same memoizing engine. It carries
// the file whose scope identifiers resolve in, so infer.Env stays file-blind.
type typeEnv struct {
	q    queries
	file FileID
}

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(e.file, id) }
func (e typeEnv) TypeOf(decl *ast.ConstDecl) ir.Type        { return e.q.typeOf(decl) }
func (e typeEnv) Registry() *builtin.Registry               { return e.q.registry() }

// exprSink wires the checking walk's findings to their diagnostics. The
// Checked stream is left unset — the const path hooks it to the eval-based
// value-range check, which needs the declaration's context.
func exprSink(at func(ast.Node) span, diags *diagnostic.List) *infer.Sink {
	return &infer.Sink{
		InvalidOp: func(node ast.Node, method, operands string) {
			s := at(node)
			diags.Add(newInvalidOperationDiagnostic(s.offset, s.width, method, operands))
		},
		Mismatch: func(node ast.Node, got, want ir.Type) {
			s := at(node)
			diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
		},
		ArityMismatch: func(lit *ast.FuncLit, got, want int) {
			s := at(lit)
			diags.Add(newLambdaArityMismatchDiagnostic(s.offset, s.width, got, want))
		},
		UninferableParam: func(p *ast.ParamDef) {
			s := at(p)
			diags.Add(newUninferableParameterDiagnostic(s.offset, s.width, p.Name))
		},
		UninferableResult: func(lit *ast.FuncLit) {
			s := at(lit)
			diags.Add(newUninferableResultDiagnostic(s.offset, s.width))
		},
	}
}

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// reference analysis and the oracle for the incremental Document.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	return assemble(soleFileID, file, positionsOf(doc.Concrete().Tree()), newDirectQueries(file, universe()))
}

// assemble builds one file's IR module and semantic diagnostics from its AST,
// using q for the resolution and typing facts; fileID names the file within
// the program, scoping its identifier resolution. It is shared by the
// reference and incremental analyzers, so they cannot diverge.
func assemble(fileID FileID, file *ast.File, positions map[cst.Green]span, q queries) (*ir.Module, []diagnostic.Diagnostic) {
	diags := &diagnostic.List{}
	at := func(n ast.Node) span { return spanOf(positions, n) }
	env := typeEnv{q: q, file: fileID}
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

	cyclic := cyclicDecls(fileID, file, q)

	for _, decl := range file.Decls {
		c := irOf[decl]
		c.Value = lower.Value(decl.Value, constBinder{q: q, file: fileID, irOf: irOf})
		c.Type = q.typeOf(decl)
		c.Eval = q.valueOf(decl)

		// Resolve the annotation with reporting enabled, so an unknown type
		// name anywhere in it (e.g. list<Bogus>) is diagnosed at its own node.
		// A file's own type declarations are not visible to a const annotation,
		// so the resolver is given no file defs.
		annType := ir.Invalid
		if decl.Type != nil {
			r := &infer.TypeResolver{Reg: reg, Report: unknownTypeReporter(at, diags)}
			annType = r.ResolveType(decl.Type, nil)
		}

		// Undefined references: every value-position identifier that resolves to
		// no declaration (method names are not value references, so walkIdents
		// skips them).
		if decl.Value != nil {
			ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
				if id.Name != "" && q.resolve(fileID, id) == nil {
					s := at(id)
					diags.Add(newUndefinedNameDiagnostic(s.offset, s.width, id.Name))
				}
			})
			// One checking walk reports the expression diagnostics: operator
			// type errors, type mismatches (against the annotation when there
			// is one, and inside function-literal bodies), and literals whose
			// parameter or result types cannot be inferred.
			sink := exprSink(at, diags)
			if annType != ir.Invalid {
				// The annotation is pushed into the value. Value-range checks
				// hook the walk's Checked stream so infer stays eval-free; the
				// top-level value is range-checked against c.Type below, so
				// only the inner expressions (collection entries, returns)
				// are checked here.
				sink.Checked = func(e ast.Expr, want ir.Type) {
					if e == decl.Value {
						return
					}
					if v := eval.Expr(e, evalEnv{q: q, file: fileID}); v != nil && v.Kind == ir.ConstInt && !types.Fits(reg, want, v.Int) {
						s := at(e)
						diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), want.String()))
					}
				}
				infer.CheckAgainst(decl.Value, annType, env, sink)
			} else {
				infer.Check(decl.Value, env, sink)
			}
			// Division or remainder by a zero divisor.
			checkDivByZero(decl.Value, evalEnv{q: q, file: fileID}, func(node ast.Node) {
				s := at(node)
				diags.Add(newDivisionByZeroDiagnostic(s.offset, s.width))
			})
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
		// An empty or heterogeneous collection literal with no annotation has
		// no type to infer (checking mode never sees it without one).
		if lit, ok := decl.Value.(*ast.CollectionLit); ok && decl.Type == nil && c.Type == ir.Invalid {
			s := at(lit)
			diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
		}
	}

	// Resolve the file's type declarations into the module's type definitions,
	// then type-check each method body against its declared result type — the
	// same checking walk the constants use, so a returned function or
	// collection literal receives the declared result type.
	module.Types = resolveTypes(file, reg, at, diags)
	checkMethodBodies(file, reg, module.Types, exprSink(at, diags))

	items := diags.Items()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Offset < items[j].Offset })
	return module, items
}

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

// cyclicDecls returns the declarations caught in a type-inference cycle. A
// declaration's type depends on the types of the value references in its
// initializer, unless an annotation fixes it; the result is a general directed
// graph (an expression may reference several names), so its cycles are found
// with a coloured depth-first search.
func cyclicDecls(fileID FileID, file *ast.File, q queries) map[*ast.ConstDecl]bool {
	deps := func(decl *ast.ConstDecl) []*ast.ConstDecl {
		if decl.Type != nil || decl.Value == nil {
			return nil // an annotation breaks the inheritance chain
		}
		var out []*ast.ConstDecl
		ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
			if t := q.resolve(fileID, id); t != nil {
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

// resolve serves the oracle's single file regardless of the file asked for;
// the multi-file Analyze (P-2 M5e) replaces this with a real per-file lookup.
func (d *directQueries) resolve(_ FileID, id *ast.Identifier) *ast.ConstDecl {
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
	t := infer.Decl(decl, typeEnv{q: d, file: soleFileID})
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
	v := computeValue(soleFileID, decl, d)
	d.valuing[decl] = false
	d.valueMemo[decl] = v
	return v
}
