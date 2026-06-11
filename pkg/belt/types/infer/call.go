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

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"

	"github.com/masterbelt/masterbelt/pkg/belt/types"
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
		return nonMemberCallType(e, s, sink)
	}
	// A member-access callee whose receiver names a namespace is a call of an
	// imported function (geo.area(...)), never a method call.
	if cands := s.fnMember(member); len(cands) > 0 {
		return funcCallType(e, member.Member.Name, cands, s, sink)
	}
	// A member-access callee whose receiver names a type is a static fn call
	// (Celsius.freezing()) — the Type.Name path, after the namespace function
	// claim and before a method call. The static overload set is selected by the
	// argument types through the same machinery a top-level function uses.
	if t, ok := staticCallType(e, member, s, sink); ok {
		return t
	}
	return methodCallType(e, member.Receiver, check(member.Receiver, s, sink), member.Member.Name, s, sink)
}

// nonMemberCallType types a call whose callee is not a member access: a
// conversion or function named by an identifier, a function-valued callee
// applied directly, an implicit self-call, or a leaf form. It is the !ok arm of
// callType, extracted so the dispatch stays flat.
func nonMemberCallType(e *ast.CallExpr, s scope, sink *Sink) ir.Type {
	// A call whose callee names a type is a conversion T(x) — the type
	// wins over a same-named function — and one that names a top-level
	// function is a function call.
	if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
		if t := s.conv(id); t != ir.Invalid {
			return convCallType(e, id.Name, t, s, sink)
		}
		if cands := s.fn(id); len(cands) > 0 {
			return funcCallType(e, id.Name, cands, s, sink)
		}
	}
	// A callee that itself types as a function value applies — a
	// function-typed constant (F(2)), a local or parameter bound to one,
	// an immediately applied literal — mirroring the folder's general
	// function-value arm: each argument checks against the function
	// type's parameter, and the call's type is its result. A callee of
	// any other type falls through to the leaf forms (whose channels
	// report an unresolved name).
	if fn, isFn := check(e.Callee, s, sink).(*ir.Func); isFn {
		return funcValueCallType(e, fn, s, sink)
	}
	// A bare call whose name is a method of self is an implicit self-call
	// (self omitted) — the form an interface's provided method uses to
	// call the required fold. It types exactly as the written self.name(...)
	// would, the same claim order the lowering's body binder uses.
	if id, isIdent := e.Callee.(*ast.Identifier); isIdent {
		if selfT := s.self(); selfT != ir.Invalid {
			if _, _, found := types.Candidates(s.registry(), selfT, id.Name); found {
				return methodCallType(e, nil, selfT, id.Name, s, sink)
			}
		}
	}
	return s.leaf(e)
}

// funcValueCallType types a call whose callee is a function value: each argument
// checks against the function type's parameter (a wrong count is an
// arity_mismatch), and the call's type is the function's result.
func funcValueCallType(e *ast.CallExpr, fn *ir.Func, s scope, sink *Sink) ir.Type {
	if len(e.Arguments) != len(fn.Params) {
		for _, a := range e.Arguments {
			check(a, s, sink)
		}
		sink.arityMismatch(e, calleeName(e.Callee), len(e.Arguments), len(fn.Params))
		return ir.Invalid
	}
	for i, a := range e.Arguments {
		checkType(a, fn.Params[i], s, map[string]ir.Type{}, sink)
	}
	return fn.Result
}

// methodCallType is the method-call half of callType: the receiver's type is
// already settled (synthesized from the member callee's receiver, or self for
// an implicit self-call — recvExpr is the receiver expression, or nil for the
// implicit form), and the call resolves the named method's overload set
// against the argument types bidirectionally.
func methodCallType(e *ast.CallExpr, recvExpr ast.Expr, recv ir.Type, method string, s scope, sink *Sink) ir.Type {
	reg := s.registry()
	bad := recv == ir.Invalid
	args := make([]ir.Type, len(e.Arguments))

	candidates, _, found := types.Candidates(reg, recv, method)
	if !found {
		return reportNoMethod(e, recv, method, args, bad, s, sink)
	}

	// Pass 1 — synthesize the non-literal arguments, left to right. The
	// function literals stay nil — they fit any parameter during selection —
	// so the overload settles before any literal is checked, and pass 2 can
	// push the winner's parameter patterns into each one. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything, so
	// the suppression style survives overloading.
	known := synthMethodArgs(e, recv, args, &bad, s, sink)

	matches, _ := types.SelectOverload(reg, recv, method, known)
	if len(matches) != 1 {
		return reportMethodOverloadFailure(e, recv, method, args, matches, candidates, bad, s, sink)
	}
	m, subst, operand := matches[0].Method, matches[0].Subst, matches[0].Operand
	// The selection among several signatures is the checker's overload
	// resolution — streamed out so the semantic layer can write it back into
	// the IR (ir.Call.Resolved) and the folder can prefer it.
	if len(candidates) > 1 {
		sink.resolvedMethod(e, m)
	}

	// Pass 2 — the function literals, each checked against its parameter
	// pattern. A finding inside the literal (a mismatch, an uninferable part)
	// fails the call without the generic report; so does an Invalid left in
	// the literal's type by a cause reported elsewhere.
	checkMethodLits(e, m, subst, args, &bad, s, sink)

	if bad {
		return ir.Invalid
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		// The settled type-variable solution — the receiver's bindings plus what
		// the argument matching solved — streamed out for the IR write-back
		// (ir.Call.Subst), on each path that types the call successfully. A call
		// that pinned nothing records nothing.
		if len(subst) > 0 {
			sink.callSubst(e, subst)
		}
		adaptedOperands(e, recvExpr, recv, m, subst, operand, args, sink)
		return operand
	}
	result := types.Substitute(m.Result, subst)
	if hasTypeVar(result) && !varsRigid(result, s) {
		// A variable no argument could solve survived to the result. (A rigid
		// variable — the enclosing declaration's own parameter — is a known
		// type within the body, not an unsolved hole.)
		sink.invalidOp(e, method, typesList(recv, args))
		return ir.Invalid
	}
	if len(subst) > 0 {
		sink.callSubst(e, subst)
	}
	adaptedOperands(e, recvExpr, recv, m, subst, operand, args, sink)
	return result
}

// reportNoMethod handles the no-such-method arm of methodCallType: synthesize
// the arguments for their own diagnostics, then report the call.
func reportNoMethod(e *ast.CallExpr, recv ir.Type, method string, args []ir.Type, bad bool, s scope, sink *Sink) ir.Type {
	for i, a := range e.Arguments {
		args[i] = check(a, s, sink)
		bad = bad || args[i] == ir.Invalid
	}
	if !bad {
		// A method call on an unbounded type parameter is the distinct
		// no_method_on_unbounded_typevar error: nothing is known about the
		// type, so it has no methods (only pass-through is allowed). A bounded
		// parameter resolves its interface's methods, so an unknown method on
		// it is an ordinary invalid_operation.
		if v, ok := recv.(*ir.TypeVar); ok && v.Bound == nil {
			sink.noMethodOnUnboundedTypeVar(e, method)
		} else {
			sink.invalidOp(e, method, typesList(recv, args))
		}
	}
	return ir.Invalid
}

// synthMethodArgs is pass 1 of methodCallType: synthesize the non-literal
// arguments left to right, filling args and the parallel known slice (nil for a
// literal or an Invalid argument, so the overload settles before any literal is
// checked). It flags bad when an argument is Invalid with no enum-member
// fallback.
func synthMethodArgs(e *ast.CallExpr, recv ir.Type, args []ir.Type, bad *bool, s scope, sink *Sink) []ir.Type {
	known := make([]ir.Type, len(e.Arguments))
	for i, a := range e.Arguments {
		if _, isLit := a.(*ast.FuncLit); isLit {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			// A bare member of the receiver's enum (rarity == Legend, desugared
			// to rarity.eql(Legend)) is that enum's value — the same channel the
			// lowering's argument binder resolves it through, tried after
			// ordinary resolution so a same-named binding still shadows the
			// member. Streamed out so the typed value graph carries it.
			if id, ok := a.(*ast.Identifier); ok {
				if mt := enumMemberExpectation(recv, id.Name); mt != nil {
					args[i] = mt
					known[i] = mt
					sink.typed(a, mt)
					continue
				}
			}
			*bad = true
			continue
		}
		known[i] = args[i]
	}
	return known
}

// reportMethodOverloadFailure handles the no-single-match arm of methodCallType:
// check the literals bare for their own diagnostics, then report the call unless
// an operand already carried its own report.
func reportMethodOverloadFailure(e *ast.CallExpr, recv ir.Type, method string, args []ir.Type, matches []types.Overload, candidates []*ir.Method, bad bool, s scope, sink *Sink) ir.Type {
	for i, a := range e.Arguments {
		if lit, isLit := a.(*ast.FuncLit); isLit {
			args[i] = check(lit, s, sink)
		}
	}
	if !bad {
		switch {
		case len(matches) > 1:
			sink.ambiguousOverload(e, method, typesList(recv, args))
		case len(candidates) > 1:
			sink.noMatchingOverload(e, method, typesList(recv, args))
		default:
			sink.invalidOp(e, method, typesList(recv, args))
		}
	}
	return ir.Invalid
}

// checkMethodLits is pass 2 of methodCallType: each function literal is checked
// against its (substituted) parameter pattern. A finding inside the literal, or
// an Invalid left in its type by a cause reported elsewhere, flags bad.
func checkMethodLits(e *ast.CallExpr, m *ir.Method, subst map[string]ir.Type, args []ir.Type, bad *bool, s scope, sink *Sink) {
	for i, a := range e.Arguments {
		lit, isLit := a.(*ast.FuncLit)
		if !isLit {
			continue
		}
		pt := types.Substitute(m.Params[i].Type, subst)
		litFailed := false
		args[i] = checkType(lit, pt, s, subst, observe(sink, &litFailed))
		if litFailed || ir.HasInvalid(args[i]) {
			*bad = true
		}
	}
}

// adaptedOperands streams the adaptions a settled method call accepted on its
// operands: each pass-1 argument whose type differs from its (substituted)
// parameter type, a self-typed argument or the receiver differing from the
// unified operand (the default integer adapting to the sized receiver, and
// vice versa). The pass-2 literal arguments streamed through their own
// checking walk, so only the synthesized ones are read here. A parameter that
// still carries an unsolved variable adapts at its monomorphized site, not
// here.
func adaptedOperands(e *ast.CallExpr, recvExpr ast.Expr, recv ir.Type, m *ir.Method, subst map[string]ir.Type, operand ir.Type, args []ir.Type, sink *Sink) {
	for i, a := range e.Arguments {
		if _, isLit := a.(*ast.FuncLit); isLit || i >= len(m.Params) {
			continue
		}
		if args[i] == nil || args[i] == ir.Invalid {
			continue
		}
		pt := types.Substitute(m.Params[i].Type, subst)
		if _, isSelf := pt.(*ir.SelfType); isSelf {
			pt = operand
		}
		if !hasTypeVar(pt) && !types.Identical(args[i], pt) {
			sink.adapted(a, pt)
		}
	}
	if recvExpr != nil && !types.Identical(recv, operand) {
		sink.adapted(recvExpr, operand)
	}
}

// convCallType is the type rule for a conversion or constructor T(x): the
// expression's type is the type the callee names, whatever its arguments. Two
// builtin types construct from arguments with value semantics: error("msg")
// from one string, and range(start, end) from two ints — each enforces its
// argument count (an arity_mismatch otherwise) and checks its arguments against
// the expected types (a mismatch is the familiar type_mismatch). Any other
// conversion's arguments are checked bare for their own findings.
func convCallType(e *ast.CallExpr, name string, t ir.Type, s scope, sink *Sink) ir.Type {
	if b, ok := t.(*ir.Builtin); ok {
		if rt, handled := builtinConvCallType(e, name, t, b, s, sink); handled {
			return rt
		}
	}
	for _, a := range e.Arguments {
		check(a, s, sink)
	}
	// A one-argument conversion to a sized integer (a builtin like short, or a
	// nominal type over one like Level) range-checks its argument: a constant
	// outside the target's value range is a constant_overflow at the conversion
	// site. The fold is the caller's (the type layer does not evaluate), so this
	// only flags the conversion for the deferred check; a non-constant argument is
	// then ignored there. Without this a value flowing into a union annotation
	// (short(70000) into short | error) would escape the const-level range check,
	// whose Fits over the union type passes through.
	if len(e.Arguments) == 1 && types.IsInteger(s.registry(), t) {
		sink.scalarConversion(e, t)
	}
	return t
}

// builtinConvCallType handles the two builtin types that construct from
// arguments with value semantics — error("msg") from one string, and range from
// two or three ints — returning handled=false for any other builtin so the
// general conversion path takes over. Each enforces its argument count (an
// arity_mismatch otherwise) and checks its arguments against the expected types.
func builtinConvCallType(e *ast.CallExpr, name string, t ir.Type, b *ir.Builtin, s scope, sink *Sink) (ir.Type, bool) {
	if n, found := s.registry().Native(b.Name); found && n.Err {
		if len(e.Arguments) != 1 {
			for _, a := range e.Arguments {
				check(a, s, sink)
			}
			sink.arityMismatch(e, name, len(e.Arguments), 1)
			return t, true
		}
		checkType(e.Arguments[0], &ir.Builtin{Name: builtin.NameString}, s, map[string]ir.Type{}, sink)
		return t, true
	}
	if b.Name == "range" {
		// range(start, end) and range(start, end, step): the integer sequence,
		// unit-step in the two-argument form and stepped in the three-argument
		// one. Each argument is an int (the same nint check the range literal's
		// bounds take); a count other than two or three is an arity_mismatch
		// (reported against two, the canonical form). A step that folds to zero
		// is the zero-step range diagnostic, raised where the value folds (the
		// semantic layer), not here — the type layer does not evaluate.
		if len(e.Arguments) != 2 && len(e.Arguments) != 3 {
			for _, a := range e.Arguments {
				check(a, s, sink)
			}
			sink.arityMismatch(e, name, len(e.Arguments), 2)
			return t, true
		}
		for _, a := range e.Arguments {
			checkType(a, &ir.Builtin{Name: builtin.NameNint}, s, map[string]ir.Type{}, sink)
		}
		return t, true
	}
	return t, false
}

// staticCallType is the type rule for a static fn call, Type.name(args). It
// reports ok=false when the callee's receiver does not name a type (a shadowing
// local or a value receiver), so the caller falls through to the method-call
// path. When the receiver names a type, the call is a static call: the type's
// static fns of that name are the overload set, selected by the argument types
// through the same funcSig machinery a top-level function uses. An unknown name
// is reported unknown_static; selection failures reuse the function-overload
// diagnostics (the name read as Type.name).
func staticCallType(e *ast.CallExpr, member *ast.MemberExpr, s scope, sink *Sink) (ir.Type, bool) {
	id, ok := member.Receiver.(*ast.Identifier)
	if !ok {
		return ir.Invalid, false
	}
	recvT := s.conv(id) // the type the receiver names, or Invalid when shadowed/unknown
	def := staticReceiverDef(recvT, s)
	if def == nil {
		return ir.Invalid, false
	}
	// A bare type name's == / != is metatype equality (Level == long, desugared to
	// Level.eql(long)): the receiver is the reified type value, so it must take the
	// method-call path (metatype receiver) even when the named type also declares a
	// static eql/neq — the metatype equality wins over the static overload set
	// rather than the static hijacking the comparison.
	if types.IsMetatypeMethod(s.universe()[builtin.NameType], member.Member.Name) {
		return ir.Invalid, false
	}
	sigs := staticSigs(def, member.Member.Name)
	if len(sigs) == 0 {
		// The receiver names a type but it has no static fn of that name. This is
		// the static call's own unknown — an enum member or associated constant of
		// the same name is a value (handled by the leaf), so reaching here means a
		// genuine call of a missing static fn.
		for _, a := range e.Arguments {
			check(a, s, sink)
		}
		sink.unknownStatic(e, member.Member.Name, def.Name)
		return ir.Invalid, true
	}
	name := def.Name + "." + member.Member.Name
	if len(sigs) == 1 {
		return checkFuncCall(e, name, sigs[0], s, sink), true
	}
	return selectFuncOverload(e, name, sigs, s, sink), true
}

// staticReceiverDef returns the definition a static call reads its fns from: a
// declared type's referent, or a primitive's registry definition (datetime
// carries the static datetime.now), so a static fn on a builtin type resolves
// like one on a declared type. Any other receiver type names no static fns.
func staticReceiverDef(recvT ir.Type, s scope) *ir.TypeDef {
	switch t := recvT.(type) {
	case *ir.Named:
		return t.Def
	case *ir.Builtin:
		return s.universe()[t.Name]
	}
	return nil
}

// staticSigs builds the funcSig overload set for the static fns named name on a
// definition: each static method's already-resolved parameter and result types,
// with self (a static fn that returns its own type written as self) resolved to
// the owning type. Static fns are not generic in the MVP, so the signatures carry
// no type parameters.
func staticSigs(def *ir.TypeDef, name string) []funcSig {
	self := ir.Type(&ir.Named{Def: def})
	var sigs []funcSig
	for _, m := range def.Methods {
		if m.Kind != ir.MethodStatic || m.Name != name {
			continue
		}
		params := make([]ir.Type, len(m.Params))
		for i, p := range m.Params {
			params[i] = substituteSelf(p.Type, self)
		}
		sigs = append(sigs, funcSig{m: m, params: params, result: substituteSelf(m.Result, self)})
	}
	return sigs
}

// substituteSelf replaces a SelfType with the owning type, leaving every other
// type unchanged — a static fn has no receiver, so a self in its signature reads
// as the type it is scoped to.
func substituteSelf(t, self ir.Type) ir.Type {
	if _, ok := t.(*ir.SelfType); ok {
		return self
	}
	return t
}

// funcSig is one resolved candidate of a function call: its declaration, its
// parameter/result types, and — for a generic function — its type parameters
// (each a TypeVar name with an optional bound). A type parameter appears as a
// TypeVar in params/result; the call solves it from the argument types (Match)
// and substitutes it into the result. Exactly one of fd (a top-level
// function's declaration) and m (a static fn's method) is set — the handle the
// overload-selection stream reports the winner through.
type funcSig struct {
	fd         *ast.FuncDecl
	m          *ir.Method
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
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified(), Registry: s.registry()}

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
		var key strings.Builder
		for i, p := range fd.Params {
			params[i] = r.ResolveType(p.Type, tscope)
			key.WriteString(typeKey(params[i]))
			key.WriteString(",")
		}
		if seen[key.String()] {
			continue
		}
		seen[key.String()] = true
		sigs = append(sigs, funcSig{fd: fd, typeParams: typeParams, params: params, result: r.ResolveType(fd.Result, tscope)})
	}

	if len(sigs) == 1 {
		// The overload set collapsed to one signature — duplicates dropped,
		// the first declaration kept callable. The survivor is still a
		// selection among several declarations, so it is streamed out: the
		// folder then applies the declaration the checker chose rather than
		// refusing the by-value-ambiguous duplicate set.
		if len(cands) > 1 {
			sink.resolvedFunc(e, sigs[0].fd)
		}
		return checkFuncCall(e, name, sigs[0], s, sink)
	}
	return selectFuncOverload(e, name, sigs, s, sink)
}

// calleeName renders a call's callee for the arity diagnostic: an identifier
// by its name, any other function-valued expression generically.
func calleeName(callee ast.Expr) string {
	if id, ok := callee.(*ast.Identifier); ok {
		return id.Name
	}
	return "function value"
}

// selectFuncOverload resolves a call against an overload set of two or more
// function-style signatures (top-level functions, or a type's static fns): it
// synthesizes the non-deferred arguments, selects the one signature the argument
// types fit, then checks the deferred arguments against the winner's parameter
// types. It reports ambiguous_func_overload / no_matching_func_overload (name
// read as the call's name) when none or several fit, and the precise mismatch
// when exactly one same-arity signature is wrong. It is shared by funcCallType
// and the static-call path so the two select identically.
func selectFuncOverload(e *ast.CallExpr, name string, sigs []funcSig, s scope, sink *Sink) ir.Type {
	// Pass 1 — synthesize the non-deferred arguments, left to right. The
	// deferred forms stay nil — they fit any parameter during selection — so
	// the overload settles before any of them is checked. An Invalid argument
	// (its cause reported at its own node) also selects as fits-anything.
	args := make([]ir.Type, len(e.Arguments))
	bad := false
	known := synthFuncArgs(e, args, &bad, s, sink)

	matches := matchFuncSigs(e, sigs, known, s)

	if len(matches) != 1 {
		return reportFuncOverloadFailure(e, name, args, matches, bad, s, sink)
	}

	// Seed the shared subst with what the non-deferred arguments pinned (Pass 1
	// synthesized them without it), so a generic parameter solved by a plain
	// argument reaches the result and the bound check — and report a known
	// argument that does not fit the winner's parameter (a type parameter a
	// later argument binds to a different type, say) as a mismatch, exactly as
	// the single-signature checkFuncCall does, rather than discarding the Match
	// result and resolving the call against whichever binding was written first.
	win := matches[0]
	// The selection among several signatures is the checker's overload
	// resolution — streamed out for the IR write-back and the folder. The
	// winner carries the handle of its kind: a top-level function's
	// declaration, or a static fn's method.
	switch {
	case win.m != nil:
		sink.resolvedStatic(e, win.m)
	case win.fd != nil:
		sink.resolvedFunc(e, win.fd)
	}
	subst := map[string]ir.Type{}
	for i, kt := range known {
		if kt == nil {
			continue
		}
		if !types.Match(s.registry(), win.params[i], kt, subst) {
			sink.mismatch(e.Arguments[i], kt, types.Substitute(win.params[i], subst))
			bad = true
		}
	}

	// Pass 2 — the deferred arguments, each checked against the winner's
	// parameter type. A finding inside one fails the call without a generic
	// report, exactly as a method call's literal arguments do.
	checkDeferredArgs(e, win, subst, args, &bad, s, sink)
	if bad {
		return ir.Invalid
	}
	// The pass-1 arguments were synthesized bare and accepted by Match; one
	// whose type differs from its settled parameter type adapted implicitly —
	// streamed for the IR's explicit Adapt, exactly as a method call's
	// operands are. (The deferred arguments streamed through their own
	// checking walk.)
	for i, kt := range known {
		if kt == nil {
			continue
		}
		if pt := types.Substitute(win.params[i], subst); !hasTypeVar(pt) && !types.Identical(kt, pt) {
			sink.adapted(e.Arguments[i], pt)
		}
	}
	// Substitute the solved type parameters into the result and run the bound and
	// uninferable checks, exactly as the single-signature path does.
	return resolveFuncResult(e, win, subst, s, sink)
}

// synthFuncArgs is pass 1 of selectFuncOverload: synthesize the non-deferred
// arguments left to right, filling args and the parallel known slice (nil for a
// deferred or Invalid argument, so the overload settles before any deferred
// form is checked). It flags bad when an argument is Invalid.
func synthFuncArgs(e *ast.CallExpr, args []ir.Type, bad *bool, s scope, sink *Sink) []ir.Type {
	known := make([]ir.Type, len(e.Arguments))
	for i, a := range e.Arguments {
		if deferredArg(a) {
			continue
		}
		args[i] = check(a, s, sink)
		if args[i] == ir.Invalid {
			*bad = true
			continue
		}
		known[i] = args[i]
	}
	return known
}

// matchFuncSigs selects the signatures the known argument types fit. When
// exactly one signature has the right arity but the arguments do not fit it, it
// falls through to that signature and lets the caller's shared-subst pass report
// the precise mismatch — the same type_mismatch a single-signature call gives,
// rather than the vaguer no_matching_func_overload (which stays for a genuine
// ambiguity: several same-arity signatures, none fitting).
func matchFuncSigs(e *ast.CallExpr, sigs []funcSig, known []ir.Type, s scope) []funcSig {
	var matches, arity []funcSig
	for _, sg := range sigs {
		if len(sg.params) != len(e.Arguments) {
			continue
		}
		arity = append(arity, sg)
		// One subst threaded across all known arguments — so a type parameter
		// pinned to int by one argument and string by another drops this
		// candidate, exactly as types.SelectOverload's per-candidate Clone does
		// for methods. A fresh map per argument (the old behavior) hid that
		// cross-argument inconsistency and made the result order-dependent.
		cand := map[string]ir.Type{}
		fits := true
		for i, kt := range known {
			if kt == nil {
				continue
			}
			if !types.Match(s.registry(), sg.params[i], kt, cand) {
				fits = false
				break
			}
		}
		if fits {
			matches = append(matches, sg)
		}
	}
	if len(matches) == 0 && len(arity) == 1 {
		matches = arity
	}
	return matches
}

// reportFuncOverloadFailure handles the no-single-match arm of
// selectFuncOverload: check the deferred arguments bare for their own
// diagnostics, then report the call unless an operand already carried its own
// report.
func reportFuncOverloadFailure(e *ast.CallExpr, name string, args []ir.Type, matches []funcSig, bad bool, s scope, sink *Sink) ir.Type {
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

// checkDeferredArgs is pass 2 of selectFuncOverload: each deferred argument is
// checked against the winner's parameter type. A finding inside one, or an
// Invalid left in its type, flags bad — without a generic report, exactly as a
// method call's literal arguments do.
func checkDeferredArgs(e *ast.CallExpr, win funcSig, subst map[string]ir.Type, args []ir.Type, bad *bool, s scope, sink *Sink) {
	for i, a := range e.Arguments {
		if !deferredArg(a) {
			continue
		}
		argFailed := false
		args[i] = checkType(a, win.params[i], s, subst, observe(sink, &argFailed))
		if argFailed || ir.HasInvalid(args[i]) {
			*bad = true
		}
	}
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
	return resolveFuncResult(e, sg, subst, s, sink)
}

// resolveFuncResult finishes a generic function call after its arguments have
// solved the type parameters into subst: each parameter must have been pinned by
// an argument (else uninferable_type_param) and its solved concrete type must
// satisfy the parameter's bound (else bound_not_satisfied), and the result is
// the declared result with the solved type parameters substituted in. A
// non-generic signature returns its result unchanged. A finding returns Invalid
// so the result never flows on with an unsolved variable. Both the
// single-signature and the overloaded paths end here, so they cannot diverge.
func resolveFuncResult(e *ast.CallExpr, sg funcSig, subst map[string]ir.Type, s scope, sink *Sink) ir.Type {
	if len(sg.typeParams) == 0 {
		return sg.result
	}
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
	// The solved substitution, streamed out for the IR write-back
	// (ir.FuncCall.Subst / ir.StaticCall.Subst) — every parameter checked
	// solved above, so the solution is complete.
	if len(subst) > 0 {
		sink.callSubst(e, subst)
	}
	return types.Substitute(sg.result, subst)
}

// FuncTypeParamScope is the set of a function's generic-parameter names, in
// scope for its bounds, parameters, result, and body — a name appearing in a
// type position there resolves to a TypeVar rather than an unknown type. The
// semantic resolver and the call site share it, so a call types a signature
// exactly as its declaration was resolved.
//
// The scope is seeded with a nil bound per name: the bounds are not yet resolved
// (a bound may name another parameter, fn first<T: foldable<U>, U>), so they are
// attached afterward by ResolveFuncTypeParams, which back-fills each name's bound
// into this same map. Resolving the parameter and result types then sees the
// bounds, so K: comparable used as map<K, V> carries the bound to the
// declaration-site check.
func FuncTypeParamScope(params []*ast.TypeParam) TypeScope {
	if len(params) == 0 {
		return nil
	}
	scope := make(TypeScope, len(params))
	for _, p := range params {
		if p.Name != "" {
			scope[p.Name] = nil
		}
	}
	return scope
}

// ResolveFuncTypeParams resolves a function's generic type parameters into
// ir.TypeParams (name plus optional resolved bound), each bound resolved in the
// full type-parameter scope so it may name a later parameter (the U in
// fn first<T: foldable<U>, U>). It also back-fills each resolved bound into the
// scope, so a subsequent resolution of the parameter and result types sees the
// bound on each TypeVar (the canonical map<K, V> with K: comparable). The bound
// is resolved against the scope before it is back-filled, so a self-referential
// bound (T: foo<T>) reads T as an unbounded variable, not recursively.
func ResolveFuncTypeParams(r *TypeResolver, params []*ast.TypeParam, scope TypeScope) []*ir.TypeParam {
	if len(params) == 0 {
		return nil
	}
	out := make([]*ir.TypeParam, len(params))
	for i, p := range params {
		out[i] = &ir.TypeParam{Name: p.Name}
	}
	backfill := func() {
		for i, p := range params {
			if p.Name != "" {
				scope[p.Name] = out[i].Bound
			}
		}
	}
	// First pass: resolve every bound silently against the seeded (unbounded)
	// scope, then back-fill. A bound may project off another parameter
	// (T: Box<U.x>), which needs that parameter's own bound; that bound is not in
	// the scope yet, so the projection fails here — but the failure is not
	// reported. It is left invalid and re-resolved below once every bound is in
	// place, so a bound that names another parameter's member resolves regardless
	// of declaration order.
	report, projErr := r.Report, r.ProjectionError
	r.Report, r.ProjectionError = nil, nil
	for i, p := range params {
		if p.Constraint != nil {
			out[i].Bound = r.ResolveType(p.Constraint, scope)
		}
	}
	r.Report, r.ProjectionError = report, projErr
	backfill()
	// Second pass: re-resolve, with reporting restored, only the bounds that stayed
	// invalid — now every other parameter's bound is in the scope, so a projection
	// off one resolves to the bound's member, while a genuinely malformed bound
	// reports its error here rather than in the throwaway first pass. The
	// parameter's own name is unbounded during its own resolution, so a
	// self-referential bound (T: foo<T>) reads it as a free variable; a bound that
	// resolved cleanly in the first pass is left untouched, so the common case is
	// unchanged.
	for i, p := range params {
		if p.Constraint == nil || !ir.HasInvalid(out[i].Bound) {
			continue
		}
		if p.Name != "" {
			scope[p.Name] = nil
		}
		out[i].Bound = r.ResolveType(p.Constraint, scope)
		if p.Name != "" {
			scope[p.Name] = out[i].Bound
		}
	}
	backfill()
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
