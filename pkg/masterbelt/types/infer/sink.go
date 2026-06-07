// Sink is the diagnostic channel of the checking walk: a struct of optional
// callbacks the walk fires its findings through, and the guarded methods that
// invoke them. Keeping the callbacks here lets the same walk serve pure typing
// (a nil Sink) and the diagnostic pass (the semantic layer's wiring) without
// the rules depending on the diagnostic types.
package infer

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Sink receives the checking walk's findings. Every field is optional and a
// nil Sink checks silently, so the same walk serves pure typing and the
// diagnostic pass; the semantic layer wires each callback to its diagnostic.
type Sink struct {
	// InvalidOp fires at the innermost method call whose operand types it is
	// not defined on, with the operand types rendered as "recv, arg, ...".
	InvalidOp func(node ast.Node, method, operands string)
	// NoMatchingOverload fires at a call whose receiver declares the method
	// under several signatures, none of which the operand types fit. (A
	// single-signature method that does not fit stays InvalidOp.)
	NoMatchingOverload func(node ast.Node, method, operands string)
	// AmbiguousOverload fires at a call whose operand types fit two or more
	// signatures of the method — resolved by annotating an operand, never by
	// an implicit priority.
	AmbiguousOverload func(node ast.Node, method, operands string)
	// Mismatch fires where got cannot be used as the expected type want. The
	// node is the offending expression — or, for a function-literal parameter
	// whose annotation conflicts with the pushed-down type, the parameter.
	Mismatch func(node ast.Node, got, want ir.Type)
	// AmbiguousUnionMember fires where a value flows into a union but two or
	// more of its members accept it with no exact tie-break, so which member it
	// tags cannot be told. got is the value's type, want the union; the fix is an
	// explicit conversion that pins the member.
	AmbiguousUnionMember func(node ast.Node, got, want ir.Type)
	// ScalarConversion fires at a conversion to a sized scalar type — T(x) where
	// T is a builtin integer or a nominal type over one — so the caller can fold
	// the argument and range-check it against T (a constant_overflow at the
	// conversion site). The check is deferred to the caller because the type layer
	// does not fold; a non-constant argument is checked-and-ignored there. call is
	// the conversion expression, target the resolved scalar type.
	ScalarConversion func(call *ast.CallExpr, target ir.Type)
	// TernaryCondNotBool fires at a ternary whose condition does not type as a
	// bool, with the condition's type.
	TernaryCondNotBool func(cond ast.Expr, got ir.Type)
	// TernaryBranchMismatch fires at a ternary whose two branches have types that
	// do not unify, with the two branch types. The node is the ternary.
	TernaryBranchMismatch func(node ast.Node, then, els ir.Type)
	// Checked fires for every (expression, expected-type) pair the checking
	// walk visits, so the semantic layer can hook value-range (Fits) checks
	// without this package depending on eval.
	Checked func(e ast.Expr, want ir.Type)
	// ArityMismatch fires at a function literal whose parameter count differs
	// from the expected function type's.
	ArityMismatch func(lit *ast.FuncLit, got, want int)
	// CallArityMismatch fires at a call of a top-level function (with a
	// single signature) with the wrong number of arguments.
	CallArityMismatch func(call *ast.CallExpr, name string, got, want int)
	// NoMatchingFuncOverload fires at a call of an overloaded function none
	// of whose signatures the argument types fit.
	NoMatchingFuncOverload func(call *ast.CallExpr, name, types string)
	// AmbiguousFuncOverload fires at a call whose argument types fit two or
	// more signatures of the function — resolved by annotating an argument,
	// never by an implicit priority.
	AmbiguousFuncOverload func(call *ast.CallExpr, name, types string)
	// UninferableParam fires at a function-literal parameter that neither has
	// an annotation nor receives a concrete type from the checking context.
	UninferableParam func(p *ast.ParamDef)
	// UninferableResult fires at a function literal that neither annotates its
	// result type nor returns a value to infer it from.
	UninferableResult func(lit *ast.FuncLit)
	// MissingField fires at a record literal that leaves a field of its record
	// type typ uninitialized.
	MissingField func(lit *ast.RecordLit, field string, typ ir.Type)
	// UnknownField fires at a field initializer whose name the record type typ
	// does not declare.
	UnknownField func(field *ast.FieldInit, name string, typ ir.Type)
	// UninferableRecord fires at an inferred-form record literal ({...}) that
	// no checking context supplies a type to.
	UninferableRecord func(lit *ast.RecordLit)
	// UnknownRecordType fires at a typed record literal whose type name
	// resolves to no definition.
	UnknownRecordType func(lit *ast.RecordLit, name string)
	// NotARecord fires at a typed record literal whose type name resolves to a
	// type that is not a record.
	NotARecord func(lit *ast.RecordLit, typ ir.Type)
	// SolvedFuncLit fires for every function literal the walk types, with its
	// settled signature — annotations, pushed-down expectations, and inferred
	// parts combined. It is informational (the editor's hover and inlay hints
	// read it), never a finding.
	SolvedFuncLit func(lit *ast.FuncLit, t *ir.Func)
	// BoundNotSatisfied fires at a generic-function call whose solved concrete
	// type for a type parameter does not implement the parameter's interface
	// bound (E-17): the argument's type carries no opt-in impl of the bound.
	BoundNotSatisfied func(call *ast.CallExpr, typ, bound ir.Type)
	// UninferableTypeParam fires at a generic-function call from which a type
	// parameter cannot be solved — no argument pins it (fn h<T>(): T) — so the
	// call has no concrete type to fold with.
	UninferableTypeParam func(call *ast.CallExpr, name string)
	// NoMethodOnUnboundedTypeVar fires at a method call whose receiver is an
	// unbounded type parameter: nothing is known about the type, so it has no
	// methods — only pass-through (receive, store, return) is allowed.
	NoMethodOnUnboundedTypeVar func(node ast.Node, method string)
	// UnknownStatic fires at a static-fn call whose receiver names a type with no
	// static fn of that name (Celsius.nope()) — the Type.name(...) twin of an
	// unknown associated constant.
	UnknownStatic func(call *ast.CallExpr, name, typ string)
	// MapKeyNotComparable fires at a map literal whose inferred key type does not
	// satisfy map's K: comparable bound — a key the language cannot compare
	// (an anonymous record). The annotation path catches the same violation
	// through the type resolver; this is the inferred-literal twin, anchored at
	// the literal so a map written without an annotation is still diagnosed.
	MapKeyNotComparable func(lit *ast.CollectionLit, key, bound ir.Type)
	// ResolvedMethod fires at a method call whose name carries several
	// signatures on the receiver and whose arguments settled exactly one — the
	// checker's overload selection, which the semantic layer writes back into
	// the IR (ir.Call.Resolved) and the folder prefers over its value-kind
	// rule. Like Checked it is an informational stream, never a finding; a
	// single-signature method needs no selection and does not fire.
	ResolvedMethod func(call *ast.CallExpr, m *ir.Method)
	// ResolvedStatic is ResolvedMethod for a static fn call Type.name(args):
	// the selected static overload individual. An informational stream.
	ResolvedStatic func(call *ast.CallExpr, m *ir.Method)
	// ResolvedFunc is ResolvedMethod for a call of an overloaded top-level
	// function: the selected declaration. An informational stream.
	ResolvedFunc func(call *ast.CallExpr, fd *ast.FuncDecl)
	// CallSubst fires for every call the walk types successfully whose
	// resolution pinned at least one type variable — the receiver's type
	// arguments combined with what the argument matching solved — with the
	// settled substitution. The semantic layer writes it back into the IR
	// (ir.Call.Subst and friends), the monomorphization input. Like Checked
	// it is an informational stream, never a finding; a call that pins no
	// variable does not fire.
	CallSubst func(call *ast.CallExpr, subst map[string]ir.Type)
	// Typed fires for every expression the walk settles with a usable (non-
	// Invalid) type — synthesized, or filled in by a pushed-down expectation.
	// It is the typed-value-graph channel: the semantic layer writes each
	// settled type back onto the IR value node the expression lowered to
	// (F-3 §2.1). An informational stream, never a finding; an expression
	// whose type never settles (its own error reported elsewhere) does not
	// fire, leaving the node's type nil — a visible hole, never an invented
	// type.
	Typed func(e ast.Expr, t ir.Type)
}

func (s *Sink) invalidOp(node ast.Node, method, operands string) {
	if s != nil && s.InvalidOp != nil {
		s.InvalidOp(node, method, operands)
	}
}

func (s *Sink) noMatchingOverload(node ast.Node, method, operands string) {
	if s != nil && s.NoMatchingOverload != nil {
		s.NoMatchingOverload(node, method, operands)
	}
}

func (s *Sink) ambiguousOverload(node ast.Node, method, operands string) {
	if s != nil && s.AmbiguousOverload != nil {
		s.AmbiguousOverload(node, method, operands)
	}
}

func (s *Sink) mismatch(node ast.Node, got, want ir.Type) {
	if s != nil && s.Mismatch != nil {
		s.Mismatch(node, got, want)
	}
}

func (s *Sink) ambiguousUnionMember(node ast.Node, got, want ir.Type) {
	if s != nil && s.AmbiguousUnionMember != nil {
		s.AmbiguousUnionMember(node, got, want)
	}
}

func (s *Sink) scalarConversion(call *ast.CallExpr, target ir.Type) {
	if s != nil && s.ScalarConversion != nil {
		s.ScalarConversion(call, target)
	}
}

func (s *Sink) ternaryCondNotBool(cond ast.Expr, got ir.Type) {
	if s != nil && s.TernaryCondNotBool != nil {
		s.TernaryCondNotBool(cond, got)
	}
}

func (s *Sink) ternaryBranchMismatch(node ast.Node, then, els ir.Type) {
	if s != nil && s.TernaryBranchMismatch != nil {
		s.TernaryBranchMismatch(node, then, els)
	}
}

func (s *Sink) checked(e ast.Expr, want ir.Type) {
	if s != nil && s.Checked != nil {
		s.Checked(e, want)
	}
}

func (s *Sink) arityMismatchLit(lit *ast.FuncLit, got, want int) {
	if s != nil && s.ArityMismatch != nil {
		s.ArityMismatch(lit, got, want)
	}
}

func (s *Sink) arityMismatch(call *ast.CallExpr, name string, got, want int) {
	if s != nil && s.CallArityMismatch != nil {
		s.CallArityMismatch(call, name, got, want)
	}
}

func (s *Sink) noMatchingFuncOverload(call *ast.CallExpr, name, types string) {
	if s != nil && s.NoMatchingFuncOverload != nil {
		s.NoMatchingFuncOverload(call, name, types)
	}
}

func (s *Sink) ambiguousFuncOverload(call *ast.CallExpr, name, types string) {
	if s != nil && s.AmbiguousFuncOverload != nil {
		s.AmbiguousFuncOverload(call, name, types)
	}
}

func (s *Sink) uninferableParam(p *ast.ParamDef) {
	if s != nil && s.UninferableParam != nil {
		s.UninferableParam(p)
	}
}

func (s *Sink) uninferableResult(lit *ast.FuncLit) {
	if s != nil && s.UninferableResult != nil {
		s.UninferableResult(lit)
	}
}

func (s *Sink) missingField(lit *ast.RecordLit, field string, typ ir.Type) {
	if s != nil && s.MissingField != nil {
		s.MissingField(lit, field, typ)
	}
}

func (s *Sink) unknownField(field *ast.FieldInit, name string, typ ir.Type) {
	if s != nil && s.UnknownField != nil {
		s.UnknownField(field, name, typ)
	}
}

func (s *Sink) uninferableRecord(lit *ast.RecordLit) {
	if s != nil && s.UninferableRecord != nil {
		s.UninferableRecord(lit)
	}
}

func (s *Sink) unknownRecordType(lit *ast.RecordLit, name string) {
	if s != nil && s.UnknownRecordType != nil {
		s.UnknownRecordType(lit, name)
	}
}

func (s *Sink) notARecord(lit *ast.RecordLit, typ ir.Type) {
	if s != nil && s.NotARecord != nil {
		s.NotARecord(lit, typ)
	}
}

func (s *Sink) solvedFuncLit(lit *ast.FuncLit, t *ir.Func) {
	if s != nil && s.SolvedFuncLit != nil {
		s.SolvedFuncLit(lit, t)
	}
}

func (s *Sink) boundNotSatisfied(call *ast.CallExpr, typ, bound ir.Type) {
	if s != nil && s.BoundNotSatisfied != nil {
		s.BoundNotSatisfied(call, typ, bound)
	}
}

func (s *Sink) uninferableTypeParam(call *ast.CallExpr, name string) {
	if s != nil && s.UninferableTypeParam != nil {
		s.UninferableTypeParam(call, name)
	}
}

func (s *Sink) noMethodOnUnboundedTypeVar(node ast.Node, method string) {
	if s != nil && s.NoMethodOnUnboundedTypeVar != nil {
		s.NoMethodOnUnboundedTypeVar(node, method)
	}
}

func (s *Sink) unknownStatic(call *ast.CallExpr, name, typ string) {
	if s != nil && s.UnknownStatic != nil {
		s.UnknownStatic(call, name, typ)
	}
}

func (s *Sink) mapKeyNotComparable(lit *ast.CollectionLit, key, bound ir.Type) {
	if s != nil && s.MapKeyNotComparable != nil {
		s.MapKeyNotComparable(lit, key, bound)
	}
}

func (s *Sink) resolvedMethod(call *ast.CallExpr, m *ir.Method) {
	if s != nil && s.ResolvedMethod != nil {
		s.ResolvedMethod(call, m)
	}
}

func (s *Sink) resolvedStatic(call *ast.CallExpr, m *ir.Method) {
	if s != nil && s.ResolvedStatic != nil {
		s.ResolvedStatic(call, m)
	}
}

func (s *Sink) resolvedFunc(call *ast.CallExpr, fd *ast.FuncDecl) {
	if s != nil && s.ResolvedFunc != nil {
		s.ResolvedFunc(call, fd)
	}
}

func (s *Sink) callSubst(call *ast.CallExpr, subst map[string]ir.Type) {
	if s != nil && s.CallSubst != nil {
		s.CallSubst(call, subst)
	}
}

func (s *Sink) typed(e ast.Expr, t ir.Type) {
	if s != nil && s.Typed != nil {
		s.Typed(e, t)
	}
}
