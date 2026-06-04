// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// Operators have already been desugared to method calls by the AST layer, so
// 1 + 2 arrives as 1.add(2). Typing and evaluation are therefore uniform: every
// expression is a literal, a value reference, or a method call, and a call's
// type and value come from a small table of builtin methods keyed by name
// (methodResult and evalMethod).
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
}

// typeEnv adapts the semantic query interface to infer.Env, so the type
// inference and checking in package types/infer can read resolution and
// declaration types through the same memoizing engine.
type typeEnv struct{ q queries }

func (e typeEnv) Resolve(id *ast.Identifier) *ast.ConstDecl { return e.q.resolve(id) }
func (e typeEnv) TypeOf(decl *ast.ConstDecl) ir.Type        { return e.q.typeOf(decl) }

// Analyze resolves and types the document's program, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// reference analysis and the oracle for the incremental Document.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	return assemble(file, positionsOf(doc.Concrete().Tree()), newDirectQueries(file))
}

// assemble builds the IR module and all semantic diagnostics from the AST, using
// q for the resolution and typing facts. It is shared by the reference and
// incremental analyzers, so they cannot diverge.
func assemble(file *ast.File, positions map[cst.Green]span, q queries) (*ir.Module, []diagnostic.Diagnostic) {
	diags := &diagnostic.List{}
	at := func(n ast.Node) span { return spanOf(positions, n) }
	env := typeEnv{q}

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
			if annType, ok := types.Lookup(decl.Type.Name); !ok {
				s := at(decl.Type)
				diags.Add(newUnknownTypeDiagnostic(s.offset, s.width, decl.Type.Name))
			} else if decl.Value != nil {
				// An annotation must agree in kind (integer vs boolean) with the
				// initializer's natural type; its value range is checked below.
				if exprT := infer.Expr(decl.Value, env); exprT != ir.Invalid && !types.Compatible(annType, exprT) {
					s := at(decl.Value)
					diags.Add(newTypeMismatchDiagnostic(s.offset, s.width, exprT.String(), annType.String()))
				}
			}
		}
		if cyclic[decl] {
			s := at(decl)
			diags.Add(newCyclicReferenceDiagnostic(s.offset, s.width, decl.Name))
		}
		// An integer value outside its concrete type's range overflows. Untyped
		// constants have no fixed range (Fits accepts them), and booleans never
		// overflow.
		if c.Eval != nil && c.Eval.Kind == ir.ConstInt && !types.Fits(c.Type, c.Eval.Int) {
			s := at(decl.Value)
			diags.Add(newConstantOverflowDiagnostic(s.offset, s.width, c.Eval.String(), c.Type.String()))
		}
	}

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
// Overflow is intentionally not checked here — untyped constants are arbitrary
// precision; the range check happens in assemble where a concrete type is known.
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
	case *ast.BoolLit:
		return ir.BoolConstant(e.Value)
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
		return evalMethod(recv, member.Member.Name, args)
	default:
		return nil
	}
}

// evalMethod evaluates a builtin operator method. It returns nil when an operand
// is unevaluated, when the operand kinds do not match the method (only reachable
// for a type-incorrect program), or on division by zero.
func evalMethod(recv *ir.Constant, method string, args []*ir.Constant) *ir.Constant {
	if recv == nil {
		return nil
	}
	for _, a := range args {
		if a == nil {
			return nil
		}
	}

	switch method {
	case "add", "sub", "mul", "div", "rem":
		if len(args) != 1 || recv.Kind != ir.ConstInt || args[0].Kind != ir.ConstInt {
			return nil
		}
		return evalArith(method, recv.Int, args[0].Int)
	case "lt", "lteq", "gt", "gteq":
		if len(args) != 1 || recv.Kind != ir.ConstInt || args[0].Kind != ir.ConstInt {
			return nil
		}
		return ir.BoolConstant(evalOrder(method, recv.Int.Cmp(args[0].Int)))
	case "eql", "neq":
		if len(args) != 1 {
			return nil
		}
		eq, ok := constEqual(recv, args[0])
		if !ok {
			return nil
		}
		if method == "neq" {
			eq = !eq
		}
		return ir.BoolConstant(eq)
	case "anan", "oror":
		if len(args) != 1 || recv.Kind != ir.ConstBool || args[0].Kind != ir.ConstBool {
			return nil
		}
		if method == "anan" {
			return ir.BoolConstant(recv.Bool && args[0].Bool)
		}
		return ir.BoolConstant(recv.Bool || args[0].Bool)
	case "pos":
		if len(args) != 0 || recv.Kind != ir.ConstInt {
			return nil
		}
		return recv
	case "neg":
		if len(args) != 0 || recv.Kind != ir.ConstInt {
			return nil
		}
		return ir.IntConstant(new(big.Int).Neg(recv.Int))
	case "not":
		if len(args) != 0 || recv.Kind != ir.ConstBool {
			return nil
		}
		return ir.BoolConstant(!recv.Bool)
	default:
		return nil
	}
}

// evalArith evaluates an integer arithmetic method, returning nil on division by
// zero. Division truncates toward zero, as the surface "/" and "%" do.
func evalArith(method string, a, b *big.Int) *ir.Constant {
	switch method {
	case "add":
		return ir.IntConstant(new(big.Int).Add(a, b))
	case "sub":
		return ir.IntConstant(new(big.Int).Sub(a, b))
	case "mul":
		return ir.IntConstant(new(big.Int).Mul(a, b))
	case "div":
		if b.Sign() == 0 {
			return nil
		}
		return ir.IntConstant(new(big.Int).Quo(a, b))
	case "rem":
		if b.Sign() == 0 {
			return nil
		}
		return ir.IntConstant(new(big.Int).Rem(a, b))
	default:
		return nil
	}
}

// evalOrder turns a big.Int comparison result into the ordering method's bool.
func evalOrder(method string, cmp int) bool {
	switch method {
	case "lt":
		return cmp < 0
	case "lteq":
		return cmp <= 0
	case "gt":
		return cmp > 0
	case "gteq":
		return cmp >= 0
	default:
		return false
	}
}

// constEqual reports whether two constants of the same kind are equal; ok is
// false when their kinds differ (a type error).
func constEqual(a, b *ir.Constant) (eq, ok bool) {
	switch {
	case a.Kind == ir.ConstInt && b.Kind == ir.ConstInt:
		return a.Int.Cmp(b.Int) == 0, true
	case a.Kind == ir.ConstBool && b.Kind == ir.ConstBool:
		return a.Bool == b.Bool, true
	default:
		return false, false
	}
}

// --- IR value lowering ------------------------------------------------------

// lowerValue builds the resolved IR value for an expression: literals map to IR
// literals, a value reference binds to its declaration's *Const, and a method
// call becomes an ir.Call with its receiver and arguments lowered recursively.
func lowerValue(e ast.Expr, irOf map[*ast.ConstDecl]*ir.Const, q queries) ir.Value {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value}
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
	syms      map[string]*ast.ConstDecl
	typeMemo  map[*ast.ConstDecl]ir.Type
	typing    map[*ast.ConstDecl]bool
	valueMemo map[*ast.ConstDecl]*ir.Constant
	valuing   map[*ast.ConstDecl]bool
}

func newDirectQueries(file *ast.File) *directQueries {
	return &directQueries{
		file:      file,
		typeMemo:  map[*ast.ConstDecl]ir.Type{},
		typing:    map[*ast.ConstDecl]bool{},
		valueMemo: map[*ast.ConstDecl]*ir.Constant{},
		valuing:   map[*ast.ConstDecl]bool{},
	}
}

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
