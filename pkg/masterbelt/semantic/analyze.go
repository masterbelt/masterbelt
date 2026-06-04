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
package semantic

import (
	"math/big"
	"sort"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
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

// ResolveType resolves a constant's type annotation — a full type expression, so
// it covers a generic type such as list<int> — against the program's type
// universe: the builtin primitives and the prelude's types (installed into the
// registry). A file's own type declarations are not yet visible to a const
// annotation, so the resolver is given no file defs. It reports nothing; the
// diagnostic pass in assemble resolves again with reporting enabled.
func (e typeEnv) ResolveType(t ast.TypeExpr) ir.Type {
	r := &typeResolver{reg: e.q.registry(), defs: map[string]*ir.TypeDef{}}
	return r.resolveType(t, nil)
}

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
		c.Value = lowerValue(decl.Value, irOf, q)
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
			r := &typeResolver{reg: reg, defs: map[string]*ir.TypeDef{}, at: at, diags: diags}
			annType := r.resolveType(decl.Type, nil)
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

// --- expression diagnostics -------------------------------------------------

// checkDivByZero reports each div/rem whose divisor folds to zero.
func checkDivByZero(e ast.Expr, q queries, report func(node ast.Node)) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	member, ok := call.Callee.(*ast.MemberExpr)
	if !ok {
		return
	}
	if (member.Member.Name == "div" || member.Member.Name == "rem") && len(call.Arguments) == 1 {
		if d := evalExpr(call.Arguments[0], q); d != nil && d.Kind == ir.ConstInt && d.Int.Sign() == 0 {
			report(call)
		}
	}
	checkDivByZero(member.Receiver, q, report)
	for _, a := range call.Arguments {
		checkDivByZero(a, q, report)
	}
}

// --- collection literals ----------------------------------------------------

// collectionChecker type-checks a collection literal against an expected type,
// element by element, reporting element type mismatches and out-of-range
// element values precisely at the offending entry.
type collectionChecker struct {
	env   typeEnv
	q     queries
	reg   *builtin.Registry
	at    func(ast.Node) span
	diags *diagnostic.List
}

// check is the entry point for a collection-valued constant. An annotated
// literal is checked against its annotation; an un-annotated one only needs its
// inferred type to be determinable (a non-empty, homogeneous literal) — an empty
// or heterogeneous one is reported as uninferable.
func (c collectionChecker) check(lit *ast.CollectionLit, annotated bool, t ir.Type) {
	if annotated {
		c.against(lit, t)
		return
	}
	if t == ir.Invalid {
		s := c.at(lit)
		c.diags.Add(newUninferableCollectionDiagnostic(s.offset, s.width))
	}
}

// against checks expression e against the expected type want: a collection
// literal must match want's shape (a list or map of the right constructor) and
// then have each entry checked against the element type; any other expression is
// checked for assignability and integer range.
func (c collectionChecker) against(e ast.Expr, want ir.Type) {
	if e == nil {
		return
	}
	if lit, ok := e.(*ast.CollectionLit); ok {
		app, isColl := collectionApp(want)
		if !isColl {
			c.mismatch(lit, want)
			return
		}
		if len(lit.Entries) > 0 && lit.IsMap() != (len(app.Args) == 2) {
			c.mismatch(lit, want) // a map literal under a list annotation, or vice versa
			return
		}
		c.entries(lit, app)
		return
	}
	if got := infer.Expr(e, c.env); want != ir.Invalid && got != ir.Invalid && !types.Assignable(c.reg, got, want) {
		s := c.at(e)
		c.diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, got.String(), want.String()))
	}
	if v := evalExpr(e, c.q); v != nil && v.Kind == ir.ConstInt && !types.Fits(c.reg, want, v.Int) {
		s := c.at(e)
		c.diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, v.String(), want.String()))
	}
}

// entries checks each entry of lit against app's element types: a list's
// elements against its one argument, a map's keys and values against its two.
func (c collectionChecker) entries(lit *ast.CollectionLit, app *ir.App) {
	switch len(app.Args) {
	case 1:
		for _, entry := range lit.Entries {
			c.against(entry.Value, app.Args[0])
		}
	case 2:
		for _, entry := range lit.Entries {
			if entry.Key != nil {
				c.against(entry.Key, app.Args[0])
			}
			c.against(entry.Value, app.Args[1])
		}
	}
}

// mismatch reports that the literal's inferred type cannot be used where want is
// expected (a non-collection annotation, or the wrong collection kind).
func (c collectionChecker) mismatch(lit *ast.CollectionLit, want ir.Type) {
	s := c.at(lit)
	c.diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, infer.Expr(lit, c.env).String(), want.String()))
}

// collectionApp returns t as a list or map application, or false if t is not a
// builtin collection type.
func collectionApp(t ir.Type) (*ir.App, bool) {
	app, ok := t.(*ir.App)
	if !ok || app.Def == nil {
		return nil, false
	}
	if app.Def.Name == "list" || app.Def.Name == "map" {
		return app, true
	}
	return nil, false
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

// --- evaluation -------------------------------------------------------------

// computeValue is the evaluation rule, shared by both query implementations.
// Overflow is intentionally not checked here — an integer literal is the
// arbitrary-precision int; the range check happens in assemble where the
// constant's concrete type is known.
func computeValue(decl *ast.ConstDecl, q queries) *ir.Constant {
	if decl.Value == nil {
		return nil
	}
	return evalExpr(decl.Value, q)
}

// evalExpr folds an expression to its constant value, or nil when it cannot be
// evaluated. Reading references through q lets the engine track dependencies and
// reuse its cycle guard.
func evalExpr(e ast.Expr, q queries) *ir.Constant {
	switch e := e.(type) {
	case *ast.IntLit:
		n, ok := new(big.Int).SetString(e.Text, 10)
		if !ok {
			return nil
		}
		return ir.IntConstant(n)
	case *ast.StringLit:
		return ir.StringConstant(e.Value)
	case *ast.BoolLit:
		return ir.BoolConstant(e.Value)
	case *ast.CollectionLit:
		return evalCollection(e, q)
	case *ast.Identifier:
		if target := q.resolve(e); target != nil {
			return q.valueOf(target)
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		recv := evalExpr(member.Receiver, q)
		args := make([]*ir.Constant, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = evalExpr(a, q)
		}
		return evalMethod(q.registry(), recv, member.Member.Name, args)
	default:
		return nil
	}
}

// evalCollection folds a collection literal: each entry's value (and key, for a
// map) is folded, in order. It returns nil if any element is unevaluated, so a
// collection with an unfoldable element does not fold to a partial value.
func evalCollection(e *ast.CollectionLit, q queries) *ir.Constant {
	entries := make([]ir.ConstEntry, 0, len(e.Entries))
	for _, entry := range e.Entries {
		var key *ir.Constant
		if entry.Key != nil {
			if key = evalExpr(entry.Key, q); key == nil {
				return nil
			}
		}
		val := evalExpr(entry.Value, q)
		if val == nil {
			return nil
		}
		entries = append(entries, ir.ConstEntry{Key: key, Value: val})
	}
	return ir.CollectionConstant(entries)
}

// evalMethod evaluates an operator method by dispatching to its native
// implementation in the builtin registry, keyed on the receiver's value kind
// (every integer type shares one set of intrinsics, every boolean type another).
// It returns nil when an operand is unevaluated, the method has no intrinsic for
// the receiver kind (only reachable for a type-incorrect program), or the
// intrinsic itself has no value (a division by zero).
func evalMethod(reg *builtin.Registry, recv *ir.Constant, method string, args []*ir.Constant) *ir.Constant {
	if recv == nil {
		return nil
	}
	for _, a := range args {
		if a == nil {
			return nil
		}
	}
	var typeName string
	switch recv.Kind {
	case ir.ConstInt:
		typeName = "int"
	case ir.ConstBool:
		typeName = "bool"
	case ir.ConstString:
		typeName = "string"
	default:
		return nil
	}
	fn, ok := reg.Intrinsic(typeName, method)
	if !ok {
		return nil
	}
	return fn(recv, args)
}

// --- IR value lowering ------------------------------------------------------

// lowerValue builds the resolved IR value for an expression: literals map to IR
// literals, a value reference binds to its declaration's *Const, and a method
// call becomes an ir.Call with its receiver and arguments lowered recursively.
func lowerValue(e ast.Expr, irOf map[*ast.ConstDecl]*ir.Const, q queries) ir.Value {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text}
	case *ast.StringLit:
		return &ir.StringLiteral{Value: e.Value}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value}
	case *ast.CollectionLit:
		entries := make([]ir.CollectionEntry, len(e.Entries))
		for i, entry := range e.Entries {
			var key ir.Value
			if entry.Key != nil {
				key = lowerValue(entry.Key, irOf, q)
			}
			entries[i] = ir.CollectionEntry{Key: key, Value: lowerValue(entry.Value, irOf, q)}
		}
		return &ir.CollectionLiteral{Entries: entries}
	case *ast.Identifier:
		if target := q.resolve(e); target != nil {
			return &ir.Reference{Target: irOf[target]}
		}
		return nil
	case *ast.CallExpr:
		member, ok := e.Callee.(*ast.MemberExpr)
		if !ok {
			return nil
		}
		args := make([]ir.Value, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = lowerValue(a, irOf, q)
		}
		return &ir.Call{Receiver: lowerValue(member.Receiver, irOf, q), Method: member.Member.Name, Args: args}
	default:
		return nil
	}
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

// --- positions --------------------------------------------------------------

type span struct{ offset, width int }

func spanOf(positions map[cst.Green]span, n ast.Node) span {
	if n == nil {
		return span{}
	}
	if s, ok := positions[n.Syntax()]; ok {
		return s
	}
	return span{}
}

// positionsOf records the offset and width of every element of the positioned
// concrete tree, keyed by its green node, so diagnostics can be anchored from a
// position-independent AST node back to its source.
func positionsOf(root cst.Tree) map[cst.Green]span {
	positions := map[cst.Green]span{}
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		positions[t.Green()] = span{offset: t.Offset(), width: t.End() - t.Offset()}
		for _, child := range t.Children() {
			walk(child)
		}
	}
	walk(root)
	return positions
}
