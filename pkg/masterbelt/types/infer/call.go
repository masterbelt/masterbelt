// This file types the call forms: a method call (callType), a type conversion
// T(x) (convCallType), and a call of a top-level function (funcCallType). Each
// drives the selection bidirectionally — synthesize the non-deferred operands,
// settle the overload, then push the winner's parameter types into the deferred
// arguments (a function literal, an inferred record literal) — so the call's
// expectation reaches into each literal and the literal bodies solve what
// remains.
package infer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// callType is the type rule for a method call, bidirectionally: the receiver
// and the non-literal arguments are synthesized first (left to right), the
// overload the argument types fit is selected (types.SelectOverload — solving
// the type variables the synthesized arguments pin down), and the
// function-literal arguments are then checked against the selected
// signature's parameter patterns, so the call's expectation reaches into each
// literal — and the literal bodies solve what remains (the R of list<T>.map).
// A nil sink types silently; with one, the innermost call whose operands fit
// no signature is reported — InvalidOp for a single-signature method,
// NoMatchingOverload/AmbiguousOverload across an overload set — except when
// the failure is inside a literal: the checking walk reported its precise
// cause already, and the Invalid propagation suppresses the pile-on (the bad
// flag).
func callType(e *ast.CallExpr, s scope, sink *Sink) ir.Type {
	member, ok := e.Callee.(*ast.MemberExpr)
	if !ok {
		// A call whose callee names a type is a conversion T(x) — the type
		// wins over a same-named function — and one that names a top-level
		// function is a function call; any other callee is nothing.
		if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
			if t := s.conv(id); t != ir.Invalid {
				return convCallType(e, id.Name, t, s, sink)
			}
			if cands := s.fn(id); len(cands) > 0 {
				return funcCallType(e, id.Name, cands, s, sink)
			}
		}
		return s.leaf(e)
	}
	// A member-access callee whose receiver names a namespace is a call of an
	// imported function (geo.area(...)), never a method call.
	if cands := s.fnMember(member); len(cands) > 0 {
		return funcCallType(e, member.Member.Name, cands, s, sink)
	}
	reg := s.registry()
	recv := check(member.Receiver, s, sink)
	bad := recv == ir.Invalid
	args := make([]ir.Type, len(e.Arguments))

	candidates, _, found := types.Candidates(reg, recv, member.Member.Name)
	if !found {
		// No such method: synthesize the arguments for their own diagnostics,
		// then report the call.
		for i, a := range e.Arguments {
			args[i] = check(a, s, sink)
			bad = bad || args[i] == ir.Invalid
		}
		if !bad {
			// A method call on an unbounded type parameter is the distinct
			// E-17 error: nothing is known about the type, so it has no methods
			// (only pass-through is allowed). A bounded parameter resolves its
			// interface's methods, so an unknown method on it is an ordinary
			// invalid_operation.
			if v, ok := recv.(*ir.TypeVar); ok && v.Bound == nil {
				sink.noMethodOnUnboundedTypeVar(e, member.Member.Name)
			} else {
				sink.invalidOp(e, member.Member.Name, typesList(recv, args))
			}
		}
		return ir.Invalid
	}

	// Pass 1 — synthesize the non-literal arguments, left to right. The
	// function literals stay nil — they fit any parameter during selection —
	// so the overload settles before any literal is checked, and pass 2 can
	// push the winner's parameter patterns into each one. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything, so
	// the suppression style survives overloading.
	known := make([]ir.Type, len(e.Arguments))
	for i, a := range e.Arguments {
		if _, isLit := a.(*ast.FuncLit); isLit {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			bad = true
			continue
		}
		known[i] = args[i]
	}

	matches, _ := types.SelectOverload(reg, recv, member.Member.Name, known)
	if len(matches) != 1 {
		// No fitting signature, or several: check the literals bare for their
		// own diagnostics, then report the call — unless an operand already
		// carried its own report.
		for i, a := range e.Arguments {
			if lit, isLit := a.(*ast.FuncLit); isLit {
				args[i] = check(lit, s, sink)
			}
		}
		if !bad {
			switch {
			case len(matches) > 1:
				sink.ambiguousOverload(e, member.Member.Name, typesList(recv, args))
			case len(candidates) > 1:
				sink.noMatchingOverload(e, member.Member.Name, typesList(recv, args))
			default:
				sink.invalidOp(e, member.Member.Name, typesList(recv, args))
			}
		}
		return ir.Invalid
	}
	m, subst, operand := matches[0].Method, matches[0].Subst, matches[0].Operand

	// Pass 2 — the function literals, each checked against its parameter
	// pattern. A finding inside the literal (a mismatch, an uninferable part)
	// fails the call without the generic report; so does an Invalid left in
	// the literal's type by a cause reported elsewhere.
	for i, a := range e.Arguments {
		lit, isLit := a.(*ast.FuncLit)
		if !isLit {
			continue
		}
		pt := types.Substitute(m.Params[i].Type, subst)
		litFailed := false
		args[i] = checkType(lit, pt, s, subst, observe(sink, &litFailed))
		if litFailed || ir.HasInvalid(args[i]) {
			bad = true
		}
	}

	if bad {
		return ir.Invalid
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return operand
	}
	result := types.Substitute(m.Result, subst)
	if hasTypeVar(result) {
		// A variable no argument could solve survived to the result.
		sink.invalidOp(e, member.Member.Name, typesList(recv, args))
		return ir.Invalid
	}
	return result
}

// convCallType is the type rule for a conversion T(x): the expression's type
// is the type the callee names, whatever its argument. The error type — the
// one natively-backed conversion with value semantics today — constructs from
// exactly one string argument, so its argument count is enforced and the
// argument checked against string (a non-string is the familiar
// type_mismatch); any other conversion's arguments are checked bare for their
// own findings.
func convCallType(e *ast.CallExpr, name string, t ir.Type, s scope, sink *Sink) ir.Type {
	if b, ok := t.(*ir.Builtin); ok {
		if n, found := s.registry().Native(b.Name); found && n.Err {
			if len(e.Arguments) != 1 {
				for _, a := range e.Arguments {
					check(a, s, sink)
				}
				sink.arityMismatch(e, name, len(e.Arguments), 1)
				return t
			}
			checkType(e.Arguments[0], &ir.Builtin{Name: "string"}, s, map[string]ir.Type{}, sink)
			return t
		}
	}
	for _, a := range e.Arguments {
		check(a, s, sink)
	}
	return t
}

// funcSig is one resolved candidate of a function call: its declaration, its
// parameter/result types, and — for a generic function — its type parameters
// (each a TypeVar name with an optional bound). A type parameter appears as a
// TypeVar in params/result; the call solves it from the argument types (Match)
// and substitutes it into the result.
type funcSig struct {
	fd         *ast.FuncDecl
	typeParams []*ir.TypeParam
	params     []ir.Type
	result     ir.Type
}

// funcCallType is the type rule for a call of a top-level function. A single
// signature checks each argument against the parameter's annotated type — so
// the expectation reaches into literal arguments, exactly as a method call's
// parameter patterns do — and reports a wrong argument count as
// arity_mismatch. An overloaded name selects the one signature the argument
// types fit, mirroring the method rules: the non-deferred arguments are
// synthesized first, the overload settles, and the deferred arguments (a
// function literal, an inferred record literal — the forms whose meaning needs
// an expectation) are then checked against the winner's parameter types. The
// signatures resolve in the scope's universe, the same one the declaration's
// own reporting pass resolves them in; an unresolved annotation was reported
// there, so an Invalid parameter or result type stays silent here.
func funcCallType(e *ast.CallExpr, name string, cands []*ast.FuncDecl, s scope, sink *Sink) ir.Type {
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}

	// Resolve every candidate's signature, dropping a later one that repeats
	// an earlier signature — the declaration pass reports the duplicate, and
	// dropping it here keeps the first one callable instead of permanently
	// ambiguous (mirroring how a duplicate method overload is dropped). A
	// generic function's type parameters are in scope for its parameter and
	// result types, so a `T` resolves to a TypeVar the call solves rather than
	// to an unknown type.
	sigs := make([]funcSig, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	for _, fd := range cands {
		tscope := FuncTypeParamScope(fd.TypeParams)
		typeParams := ResolveFuncTypeParams(r, fd.TypeParams, tscope)
		params := make([]ir.Type, len(fd.Params))
		key := ""
		for i, p := range fd.Params {
			params[i] = r.ResolveType(p.Type, tscope)
			key += typeKey(params[i]) + ","
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		sigs = append(sigs, funcSig{fd: fd, typeParams: typeParams, params: params, result: r.ResolveType(fd.Result, tscope)})
	}

	if len(sigs) == 1 {
		return checkFuncCall(e, name, sigs[0], s, sink)
	}

	// Pass 1 — synthesize the non-deferred arguments, left to right. The
	// deferred forms stay nil — they fit any parameter during selection — so
	// the overload settles before any of them is checked. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything.
	args := make([]ir.Type, len(e.Arguments))
	known := make([]ir.Type, len(e.Arguments))
	bad := false
	for i, a := range e.Arguments {
		if deferredArg(a) {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			bad = true
			continue
		}
		known[i] = args[i]
	}

	var matches []funcSig
	for _, sg := range sigs {
		if len(sg.params) != len(e.Arguments) {
			continue
		}
		fits := true
		for i, kt := range known {
			if kt == nil {
				continue
			}
			if !types.Match(s.registry(), sg.params[i], kt, map[string]ir.Type{}) {
				fits = false
				break
			}
		}
		if fits {
			matches = append(matches, sg)
		}
	}

	if len(matches) != 1 {
		// No fitting signature, or several: check the deferred arguments bare
		// for their own diagnostics, then report the call — unless an operand
		// already carried its own report.
		for i, a := range e.Arguments {
			if deferredArg(a) {
				args[i] = check(a, s, sink)
			}
		}
		if !bad {
			if len(matches) > 1 {
				sink.ambiguousFuncOverload(e, name, argTypesList(args))
			} else {
				sink.noMatchingFuncOverload(e, name, argTypesList(args))
			}
		}
		return ir.Invalid
	}

	// Pass 2 — the deferred arguments, each checked against the winner's
	// parameter type. A finding inside one fails the call without a generic
	// report, exactly as a method call's literal arguments do.
	win := matches[0]
	subst := map[string]ir.Type{}
	for i, a := range e.Arguments {
		if !deferredArg(a) {
			continue
		}
		argFailed := false
		args[i] = checkType(a, win.params[i], s, subst, observe(sink, &argFailed))
		if argFailed || ir.HasInvalid(args[i]) {
			bad = true
		}
	}
	if bad {
		return ir.Invalid
	}
	return win.result
}

// checkFuncCall types a call of a single-signature function, generic or not.
// The arity is checked first; then each argument is checked against its
// parameter type with a shared substitution, which both pushes the expectation
// into a function-literal argument and solves the function's type parameters
// (Match, the same mechanism list.map's element type uses). After the arguments
// settle, each type parameter must be solved (else uninferable_type_param) and
// its solved concrete type must satisfy the parameter's bound (else
// bound_not_satisfied). The result is the declared result with the solved type
// parameters substituted in.
func checkFuncCall(e *ast.CallExpr, name string, sg funcSig, s scope, sink *Sink) ir.Type {
	if len(e.Arguments) != len(sg.params) {
		for _, a := range e.Arguments {
			check(a, s, sink) // the arguments still check bare for their own diagnostics
		}
		sink.arityMismatch(e, name, len(e.Arguments), len(sg.params))
		return ir.Invalid
	}
	subst := map[string]ir.Type{}
	for i, a := range e.Arguments {
		checkType(a, sg.params[i], s, subst, sink)
	}
	// A non-generic function is done: its result carries no type parameters.
	if len(sg.typeParams) == 0 {
		return sg.result
	}
	// Each type parameter must have been pinned by an argument, and its solved
	// concrete type must satisfy the bound. A finding here returns Invalid so
	// the result does not flow on with an unsolved variable.
	ok := true
	for _, tp := range sg.typeParams {
		solved, found := subst[tp.Name]
		if !found || hasTypeVar(solved) {
			sink.uninferableTypeParam(e, tp.Name)
			ok = false
			continue
		}
		if tp.Bound != nil && !types.Satisfies(s.registry(), solved, tp.Bound) {
			sink.boundNotSatisfied(e, solved, tp.Bound)
			ok = false
		}
	}
	if !ok {
		return ir.Invalid
	}
	return types.Substitute(sg.result, subst)
}

// FuncTypeParamScope is the set of a function's generic-parameter names, in
// scope for its bounds, parameters, result, and body — a name appearing in a
// type position there resolves to a TypeVar rather than an unknown type. The
// semantic resolver and the call site share it, so a call types a signature
// exactly as its declaration was resolved.
func FuncTypeParamScope(params []*ast.TypeParam) map[string]bool {
	if len(params) == 0 {
		return nil
	}
	scope := make(map[string]bool, len(params))
	for _, p := range params {
		if p.Name != "" {
			scope[p.Name] = true
		}
	}
	return scope
}

// ResolveFuncTypeParams resolves a function's generic type parameters into
// ir.TypeParams (name plus optional resolved bound), each bound resolved in the
// full type-parameter scope so it may name a later parameter (the U in
// fn first<T: foldable<U>, U>).
func ResolveFuncTypeParams(r *TypeResolver, params []*ast.TypeParam, scope map[string]bool) []*ir.TypeParam {
	if len(params) == 0 {
		return nil
	}
	out := make([]*ir.TypeParam, 0, len(params))
	for _, p := range params {
		var bound ir.Type
		if p.Constraint != nil {
			bound = r.ResolveType(p.Constraint, scope)
		}
		out = append(out, &ir.TypeParam{Name: p.Name, Bound: bound})
	}
	return out
}

// BindTypeParamBounds is the substitution that replaces each bare type-parameter
// variable with its bounded form: a name resolved in a type-parameter scope is
// an unbounded TypeVar, so a function body that types `c: T` (where T has a
// bound) must rebind T to the bounded TypeVar to see the bound interface's
// methods. The result maps each name to a TypeVar carrying its bound; apply it
// with types.Substitute over the resolved parameter and result types.
func BindTypeParamBounds(typeParams []*ir.TypeParam) map[string]ir.Type {
	if len(typeParams) == 0 {
		return nil
	}
	subst := make(map[string]ir.Type, len(typeParams))
	for _, tp := range typeParams {
		subst[tp.Name] = &ir.TypeVar{Name: tp.Name, Bound: tp.Bound}
	}
	return subst
}

// deferredArg reports whether an argument's typing needs the parameter's
// expectation — a function literal, or an inferred-form record literal — so
// overload selection must not synthesize it.
func deferredArg(a ast.Expr) bool {
	switch a := a.(type) {
	case *ast.FuncLit:
		return true
	case *ast.RecordLit:
		return a.TypeName == ""
	default:
		return false
	}
}

// typeKey renders a parameter type for the duplicate-signature key; nil-safe
// for a recovered annotation.
func typeKey(t ir.Type) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

// argTypesList renders the argument types as "a, b" for the overload
// diagnostics.
func argTypesList(args []ir.Type) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = typeKey(a)
	}
	return strings.Join(parts, ", ")
}
