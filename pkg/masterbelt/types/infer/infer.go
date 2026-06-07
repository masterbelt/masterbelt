// Package infer is the syntax-driven half of masterbelt's type system: it
// derives the type of an expression or declaration by walking the AST, and
// checks an expression for operator-method type errors. Where package types is
// the pure algebra over a type value (no syntax), infer applies that algebra to
// the tree.
//
// One walk (exprType) types every expression. The forms shared by every context
// — int, string, and boolean literals, collection literals, and method calls —
// are typed here uniformly; the forms whose meaning depends on context — a value
// name, the receiver self, a record field access, a conversion, the null literal
// — are delegated to a scope. A constant initializer (Expr/Decl) and a method
// body (Body) are the same walk over two scopes, so the collection and method
// rules are written once.
//
// Inference reads name resolution and declaration types through an Env, so it
// has no dependency on the semantic query engine — the engine supplies a
// memoizing Env, but the rules here are a pure function of the AST and that
// environment, which is what makes them testable in isolation.
package infer

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/types"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Env is what inference and checking need from their driver: name resolution,
// the type of a referenced declaration, the type universe (to resolve a type
// annotation), and the builtin registry (to type operator-method calls). Keeping
// this an interface lets the semantic engine supply a memoizing implementation
// (so an identifier's type is computed once and dependencies are tracked) while
// this package stays a pure set of rules.
type Env interface {
	// Resolve returns the declaration a value-position identifier refers to, or
	// nil if no declaration has that name.
	Resolve(id *ast.Identifier) *ast.ConstDecl
	// ResolveMember returns the declaration a namespace member access
	// (geo.Origin) refers to, or nil when the receiver names no namespace or
	// the member is not among the namespace's exported values.
	ResolveMember(m *ast.MemberExpr) *ast.ConstDecl
	// ResolveFunc returns the overload set a call's callee name refers to —
	// every same-name function declaration, in source order — or nil if no
	// function has that name.
	ResolveFunc(id *ast.Identifier) []*ast.FuncDecl
	// ResolveFuncMember returns the overload set a namespace function call
	// (geo.area(...)) refers to: the namespace target's exported functions of
	// that name, or nil.
	ResolveFuncMember(m *ast.MemberExpr) []*ast.FuncDecl
	// TypeOf returns a declaration's type (ir.Invalid when undeterminable).
	TypeOf(decl *ast.ConstDecl) ir.Type
	// Universe returns the named type definitions annotations resolve in: the
	// file's own type declarations shadowing its imported ones.
	Universe() map[string]*ir.TypeDef
	// QualifiedType resolves a namespace-qualified type name (geo.Point) to
	// the definition the namespace's target exports, or nil.
	QualifiedType(namespace, name string) *ir.TypeDef
	// Registry returns the builtin registry the program types against.
	Registry() *builtin.Registry
}

// scope is the typing context an expression is walked in. It owns the registry
// (so method and collection rules can reach the builtin types) and types the
// context-specific leaf forms, recursing into sub-expressions through the same
// walk (exprType) so it sees the scope's own rules.
type scope interface {
	registry() *builtin.Registry
	// universe is the named type definitions annotations resolve in within
	// this scope.
	universe() map[string]*ir.TypeDef
	// qualified is the namespace-qualified type lookup in effect within this
	// scope (nil when no namespaces are in scope).
	qualified() func(namespace, name string) *ir.TypeDef
	// leaf types an expression form whose meaning is context-specific — a value
	// name, self, a field access, a conversion, the null literal — returning
	// ir.Invalid when the form is not meaningful in this scope.
	leaf(e ast.Expr) ir.Type
	// conv resolves a call's callee name to the type a conversion T(x) names,
	// or ir.Invalid when the name is not a type in this scope (a parameter
	// shadows a same-named type). A type name wins over a same-named function.
	conv(id *ast.Identifier) ir.Type
	// fn resolves a call's callee name to the overload set of the top-level
	// function it names, or nil when nothing of that name is callable here —
	// a parameter shadows a same-named function.
	fn(id *ast.Identifier) []*ast.FuncDecl
	// fnMember resolves a call's member-access callee (geo.area) to the
	// overload set the namespace's target exports, or nil when the receiver
	// names no namespace (a value's method call).
	fnMember(m *ast.MemberExpr) []*ast.FuncDecl
}

// Decl is the type rule for a declaration: an annotation gives a concrete type,
// otherwise the type is inferred from the initializer expression. It reads other
// declarations' types through env so a memoizing engine can track the
// dependencies. The annotation resolves in env's universe — the file's own type
// declarations and its imports, over the registry — silently; the diagnostic
// pass resolves it again with reporting enabled.
func Decl(decl *ast.ConstDecl, env Env) ir.Type {
	if decl.Type != nil {
		r := &TypeResolver{Defs: env.Universe(), Qualified: env.QualifiedType}
		return r.ResolveType(decl.Type, nil)
	}
	if decl.Value == nil {
		return ir.Invalid
	}
	return Expr(decl.Value, env)
}

// Expr infers the type of a constant initializer: an integer literal is int, a
// string literal string, a boolean literal bool, a value reference inherits its
// referent's type, and a method call's type comes from the builtin method
// rules driven bidirectionally (callType), so a function-literal argument is
// typed against the parameter it is passed to.
func Expr(e ast.Expr, env Env) ir.Type {
	return exprType(e, constScope{env})
}

// exprType is the one inference walk. The shared forms are typed here; the
// context-specific leaves go through scope.leaf.
func exprType(e ast.Expr, s scope) ir.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.Builtin{Name: "nint"}
	case *ast.StringLit:
		return &ir.Builtin{Name: "string"}
	case *ast.BoolLit:
		return &ir.Builtin{Name: "bool"}
	case *ast.DatetimeLit:
		return &ir.Builtin{Name: "datetime"}
	case *ast.DurationLit:
		return &ir.Builtin{Name: "duration"}
	case *ast.CollectionLit:
		return collectionType(e, s)
	case *ast.RecordLit:
		return recordLitType(e, s)
	case *ast.FuncLit:
		return funcLitType(e, s)
	case *ast.AwaitExpr:
		// await marks the suspension point and adds nothing to the type.
		if e.Value == nil {
			return ir.Invalid
		}
		return exprType(e.Value, s)
	case *ast.TernaryExpr:
		// The ternary's type is its two branches' unified type; the silent walk
		// reports nothing (the checking twin does), so it only synthesizes.
		return ternaryType(e, s)
	case *ast.RangeExpr:
		// A range literal is the range builtin whatever its bounds; the bound types
		// (each must be an integer) are the checking twin's concern.
		return rangeBuiltin()
	case *ast.CallExpr:
		// A call through a member access is a method call; any other callee is a
		// context-specific form (a conversion in a method body, otherwise nothing).
		return callType(e, s, nil)
	default:
		return s.leaf(e)
	}
}

// rangeBuiltin is the type of a range literal — the range builtin, the same type
// the range(...) constructor produces. A literal's bounds do not change its type
// (every range is a range<nint, nint> over nint), so this needs no scope.
func rangeBuiltin() ir.Type {
	return &ir.Builtin{Name: "range"}
}

// ternaryType is the silent type of a conditional value: the unified type of its
// two branches. The condition's type and a branch mismatch are the checking
// twin's to report; here a branch that is Invalid or a non-unifying pair yields
// ir.Invalid, exactly as a collection literal's element merge does.
func ternaryType(e *ast.TernaryExpr, s scope) ir.Type {
	if e.Then == nil || e.Else == nil {
		return ir.Invalid
	}
	then := exprType(e.Then, s)
	els := exprType(e.Else, s)
	if then == ir.Invalid || els == ir.Invalid {
		return ir.Invalid
	}
	return types.Unify(s.registry(), then, els)
}

// recordLitType is the silent type of a record literal: the named record type
// of the typed form (Point{...}). The inferred form ({...}) has no type of its
// own — only a checking context can supply one — and a name that is unknown or
// not a record carries none either, so all three are ir.Invalid here; the
// checking twin (checkRecordLit) reports each cause.
func recordLitType(e *ast.RecordLit, s scope) ir.Type {
	if e.TypeName == "" {
		return ir.Invalid
	}
	typ, rec := namedRecord(e.TypeName, s)
	if rec == nil {
		return ir.Invalid
	}
	return typ
}

// namedRecord resolves a record literal's type name in the scope's universe:
// the type it names (nil when the name is unknown) and its underlying record
// (nil when the type is not a record).
func namedRecord(name string, s scope) (ir.Type, *ir.Record) {
	def, ok := s.universe()[name]
	if !ok {
		return nil, nil
	}
	var t ir.Type
	if def.Builtin {
		t = &ir.Builtin{Name: def.Name}
	} else {
		t = &ir.Named{Def: def}
	}
	return t, recordOf(t)
}

// funcLitType is the type of a function-literal expression: the Func type built
// from its declared parameter types and its declared — or, when the annotation
// is omitted, inferred — result type. The annotations resolve in the scope's
// universe — the same one a constant annotation resolves in — so file-local
// and imported type names work in a literal's signature exactly as they do on
// a declaration. An omitted result type is synthesized from the body's return
// values, typed in a funcScope over s; an omitted parameter type is ir.Invalid
// here (only a checking context can supply it).
func funcLitType(e *ast.FuncLit, s scope) ir.Type {
	r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
	params := make([]ir.Type, len(e.Params))
	names := make(map[string]ir.Type, len(e.Params))
	for i, p := range e.Params {
		params[i] = r.ResolveType(p.Type, nil)
		names[p.Name] = params[i]
	}
	result := r.ResolveType(e.Result, nil)
	if e.Result == nil {
		result = returnedType(e.Body, funcScope{outer: s, params: names})
	}
	return &ir.Func{Params: params, Result: result}
}

// returnedType synthesizes a function body's result type: the unified type of
// every returned value. It descends the full statement grammar — through an
// if's branches and a switch's arm bodies — so a return nested in control flow
// participates, and threads the let locals a block introduces so a return of a
// let local resolves it. This is the silent twin of synthesizeReturns; the two
// must agree, so funcLitType (the IR signature) matches checkFuncLit's. A body
// that returns nothing — or whose returns do not unify — has no synthesizable
// result and is ir.Invalid.
func returnedType(body []ast.Stmt, s funcScope) ir.Type {
	result := returnedTypeIn(body, s)
	if result == nil {
		return ir.Invalid
	}
	return result
}

// returnedTypeIn walks a statement body, unifying the type of every return it
// reaches (nil when there is no return, ir.Invalid when the returns conflict)
// and threading the let locals each block introduces. A nested block walks a
// copy of the scope, so a branch's let does not leak out.
func returnedTypeIn(body []ast.Stmt, s funcScope) ir.Type {
	var result ir.Type
	merge := func(t ir.Type) {
		if t == nil {
			return
		}
		if result == nil {
			result = t
		} else {
			result = types.Unify(s.registry(), result, t)
		}
	}
	for _, stmt := range body {
		switch stmt := stmt.(type) {
		case *ast.ReturnStmt:
			if stmt.Value != nil {
				merge(exprType(stmt.Value, s))
			}
		case *ast.LetStmt:
			s = letScope(stmt, s)
		case *ast.IfStmt:
			merge(returnedTypeInIf(stmt, s))
		case *ast.SwitchStmt:
			for _, arm := range stmt.Arms {
				merge(returnedTypeIn(arm.Body, s))
			}
			merge(returnedTypeIn(stmt.Else, s))
			for _, arm := range stmt.AfterElse {
				merge(returnedTypeIn(arm.Body, s))
			}
		case *ast.MatchStmt:
			// Each arm body is walked in the scope where its binding is narrowed
			// to the arm's member type, so a return that reads the binding
			// synthesizes against the narrowed type — the same scope walkMatch and
			// the IR lowering use.
			for _, arm := range stmt.Arms {
				merge(returnedTypeIn(arm.Body, narrowArmScope(s, arm)))
			}
			merge(returnedTypeIn(stmt.Else, s))
			for _, arm := range stmt.AfterElse {
				merge(returnedTypeIn(arm.Body, narrowArmScope(s, arm)))
			}
		case *ast.ForStmt:
			// The loop body is walked in the scope where the loop variable is bound
			// to the iter's element type, so a return that reads the variable
			// synthesizes against it — the same scope walkFor and the IR lowering use.
			merge(returnedTypeIn(stmt.Body, forScope(s, stmt)))
		case *ast.ExprStmt, *ast.AssignStmt:
			// Neither yields a return nor binds a new local that a later return
			// reads, so neither changes the inferred result. Listed so a new
			// statement kind hits the default instead of being silently ignored.
		default:
			panic(ast.UnhandledStmt(stmt))
		}
	}
	return result
}

// returnedTypeInIf unifies the return types reached through an if's then body,
// its else-if chain, and its else body.
func returnedTypeInIf(stmt *ast.IfStmt, s funcScope) ir.Type {
	var result ir.Type
	merge := func(t ir.Type) {
		if t == nil {
			return
		}
		if result == nil {
			result = t
		} else {
			result = types.Unify(s.registry(), result, t)
		}
	}
	merge(returnedTypeIn(stmt.Then, s))
	if stmt.ElseIf != nil {
		merge(returnedTypeInIf(stmt.ElseIf, s))
	}
	merge(returnedTypeIn(stmt.Else, s))
	return result
}

// letScope returns the scope extended with a let's local at its settled type —
// the annotation when written, otherwise the value's inferred type — the silent
// counterpart of walkLet, so the silent and reporting walks bind a local the
// same way.
func letScope(stmt *ast.LetStmt, s funcScope) funcScope {
	if stmt.Name == "" {
		return s
	}
	var typ ir.Type
	switch {
	case stmt.Type != nil:
		r := &TypeResolver{Defs: s.universe(), Qualified: s.qualified()}
		typ = r.ResolveType(stmt.Type, nil)
	case stmt.Value != nil:
		typ = exprType(stmt.Value, s)
	default:
		typ = ir.Invalid
	}
	return s.withLocal(stmt.Name, typ)
}

// collectionType infers a collection literal's type: list<E> from the unified
// element type, or map<K, V> from the unified key and value types. An empty
// literal has no entries to infer from, so its type comes from the annotation,
// not from here — it returns ir.Invalid. A literal whose entries do not unify
// (mismatched element types) is ir.Invalid too.
func collectionType(e *ast.CollectionLit, s scope) ir.Type {
	if len(e.Entries) == 0 {
		return ir.Invalid
	}
	reg := s.registry()
	if e.IsMap() {
		def, ok := reg.Lookup("map")
		if !ok {
			return ir.Invalid
		}
		keyT, valT := ir.Type(nil), ir.Type(nil)
		for i, entry := range e.Entries {
			k, v := exprType(entry.Key, s), exprType(entry.Value, s)
			if i == 0 {
				keyT, valT = k, v
			} else {
				keyT, valT = types.Unify(reg, keyT, k), types.Unify(reg, valT, v)
			}
		}
		if keyT == ir.Invalid || valT == ir.Invalid {
			return ir.Invalid
		}
		return &ir.App{Def: def, Args: []ir.Type{keyT, valT}}
	}
	def, ok := reg.Lookup("list")
	if !ok {
		return ir.Invalid
	}
	var elemT ir.Type
	for i, entry := range e.Entries {
		t := exprType(entry.Value, s)
		if i == 0 {
			elemT = t
		} else {
			elemT = types.Unify(reg, elemT, t)
		}
	}
	if elemT == ir.Invalid {
		return ir.Invalid
	}
	return &ir.App{Def: def, Args: []ir.Type{elemT}}
}

// fieldType returns the type of a record's field, following named types to their
// underlying record.
func fieldType(recv ir.Type, name string) ir.Type {
	rec := recordOf(recv)
	if rec == nil {
		return ir.Invalid
	}
	for _, f := range rec.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return ir.Invalid
}

// memberReadType types a value member read value.name: a record field when the
// receiver has one of that name, otherwise a getter the receiver declares. The
// field reading wins (a collision is rejected at the getter's declaration), so
// the order matches the declaration check's guarantee that the two never share a
// name. It is the last reading the value-scope leaf falls through to, after the
// Type.Name paths (enum member, associated constant) the receiver-as-type forms
// take. The result is ir.Invalid when neither a field nor a getter matches.
func memberReadType(reg *builtin.Registry, recv ir.Type, name string) ir.Type {
	if t := fieldType(recv, name); t != ir.Invalid {
		return t
	}
	return getterType(reg, recv, name)
}

// getterType returns the type a getter read value.name produces: the getter's
// result, with self resolving to the receiver (a getter that returns self yields
// the receiver's type, exactly as a self-returning method does). It is
// ir.Invalid when the receiver declares no getter of that name.
func getterType(reg *builtin.Registry, recv ir.Type, name string) ir.Type {
	m, subst, ok := types.Getter(reg, recv, name)
	if !ok {
		return ir.Invalid
	}
	if _, isSelf := m.Result.(*ir.SelfType); isSelf {
		return recv
	}
	return types.Substitute(m.Result, subst)
}

func recordOf(t ir.Type) *ir.Record {
	switch t := t.(type) {
	case *ir.Record:
		return t
	case *ir.Named:
		if t.Def != nil {
			return recordOf(t.Def.Body)
		}
	}
	return nil
}

// enumMemberType types a member access whose receiver names an enum: the enum's
// Named type when the receiver resolves to an enum definition in universe that
// declares the member, ir.Invalid otherwise (an unknown enum, a non-enum
// receiver, or an unknown member). A member access that is not an enum access
// falls through to the scope's other readings (a namespace import, a record
// field).
func enumMemberType(universe map[string]*ir.TypeDef, m *ast.MemberExpr) ir.Type {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return ir.Invalid
	}
	def, ok := universe[recv.Name]
	if !ok || def.Enum == nil {
		return ir.Invalid
	}
	for _, member := range def.Enum.Members {
		if member.Name == m.Member.Name {
			return &ir.Named{Def: def}
		}
	}
	return ir.Invalid
}

// assocConstType types a member access whose receiver names a type and whose
// member names one of that type's associated constants (int8.Max, Level.Max):
// the constant's resolved type. It returns ir.Invalid when the receiver names
// no known type or the type has no such associated constant — the same fall-
// through enumMemberType gives, so the two share the one Type.Name path. A type
// access wins over a record-field reading, exactly as an enum member does.
func assocConstType(universe map[string]*ir.TypeDef, m *ast.MemberExpr) ir.Type {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return ir.Invalid
	}
	def, ok := universe[recv.Name]
	if !ok {
		return ir.Invalid
	}
	for _, c := range def.Consts {
		if c.Name == m.Member.Name {
			if c.Type == nil {
				return ir.Invalid
			}
			return c.Type
		}
	}
	return ir.Invalid
}

// enumMemberIndex returns the index of the named member of an enum definition,
// or -1 when def is not an enum or has no such member.
func enumMemberIndex(def *ir.TypeDef, name string) int {
	if def == nil || def.Enum == nil {
		return -1
	}
	for i, m := range def.Enum.Members {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// enumMemberExpectation resolves a bare member name against an enum expectation:
// the named type of the enum want carries (the enum itself, a union carrying it,
// or — through types.EnumDef's union unwrapping — a named or generic union alias
// of one) when that enum declares the name, nil otherwise. A nil return falls
// through to ordinary identifier resolution. Sharing types.EnumDef makes a bare
// member resolve under an alias expectation (optional<Rarity>) exactly as it does
// under the bare enum.
func enumMemberExpectation(want ir.Type, name string) ir.Type {
	if def := types.EnumDef(want); def != nil && enumMemberIndex(def, name) >= 0 {
		return &ir.Named{Def: def}
	}
	return nil
}

// hasTypeVar reports whether t still contains a type variable — i.e. the
// checking context has not pinned every generic part to a concrete type.
func hasTypeVar(t ir.Type) bool {
	switch t := t.(type) {
	case *ir.TypeVar:
		return true
	case *ir.App:
		for _, a := range t.Args {
			if hasTypeVar(a) {
				return true
			}
		}
	case *ir.Func:
		for _, p := range t.Params {
			if hasTypeVar(p) {
				return true
			}
		}
		return hasTypeVar(t.Result)
	case *ir.Union:
		for _, m := range t.Members {
			if hasTypeVar(m) {
				return true
			}
		}
	case *ir.Record:
		for _, f := range t.Fields {
			if hasTypeVar(f.Type) {
				return true
			}
		}
	}
	return false
}

// observe wraps sink so the caller learns whether the wrapped walk reported a
// finding. It re-wraps every finding callback to flip *fired and then delegate
// through sink's guarded method (which no-ops for a nil sink or a nil field), so
// a finding sets *fired whether or not the wrapped sink renders it — that is what
// lets the silent typing walk (a nil sink) share the call rule and still learn a
// lambda argument failed. The informational streams (Checked, SolvedFuncLit, and
// the Resolved* overload selections) are not findings: they are forwarded as-is
// and never flip *fired.
//
// Every finding callback is wrapped unconditionally — the wrapper does not look
// at whether sink set the field — so the list here is the single point that must
// track the Sink struct. TestObserveForwardsEverySinkField reflects over Sink and
// fails if any field is not forwarded, so a callback added to Sink cannot be
// dropped on this path again.
func observe(sink *Sink, fired *bool) *Sink {
	return &Sink{
		// Findings: flip *fired, then delegate through the guarded method.
		InvalidOp: func(node ast.Node, method, operands string) {
			*fired = true
			sink.invalidOp(node, method, operands)
		},
		NoMatchingOverload: func(node ast.Node, method, operands string) {
			*fired = true
			sink.noMatchingOverload(node, method, operands)
		},
		AmbiguousOverload: func(node ast.Node, method, operands string) {
			*fired = true
			sink.ambiguousOverload(node, method, operands)
		},
		Mismatch: func(node ast.Node, got, want ir.Type) {
			*fired = true
			sink.mismatch(node, got, want)
		},
		AmbiguousUnionMember: func(node ast.Node, got, want ir.Type) {
			*fired = true
			sink.ambiguousUnionMember(node, got, want)
		},
		ScalarConversion: func(call *ast.CallExpr, target ir.Type) {
			*fired = true
			sink.scalarConversion(call, target)
		},
		TernaryCondNotBool: func(cond ast.Expr, got ir.Type) {
			*fired = true
			sink.ternaryCondNotBool(cond, got)
		},
		TernaryBranchMismatch: func(node ast.Node, then, els ir.Type) {
			*fired = true
			sink.ternaryBranchMismatch(node, then, els)
		},
		ArityMismatch: func(lit *ast.FuncLit, got, want int) {
			*fired = true
			sink.arityMismatchLit(lit, got, want)
		},
		CallArityMismatch: func(call *ast.CallExpr, name string, got, want int) {
			*fired = true
			sink.arityMismatch(call, name, got, want)
		},
		NoMatchingFuncOverload: func(call *ast.CallExpr, name, types string) {
			*fired = true
			sink.noMatchingFuncOverload(call, name, types)
		},
		AmbiguousFuncOverload: func(call *ast.CallExpr, name, types string) {
			*fired = true
			sink.ambiguousFuncOverload(call, name, types)
		},
		UninferableParam: func(p *ast.ParamDef) {
			*fired = true
			sink.uninferableParam(p)
		},
		UninferableResult: func(lit *ast.FuncLit) {
			*fired = true
			sink.uninferableResult(lit)
		},
		MissingField: func(lit *ast.RecordLit, field string, typ ir.Type) {
			*fired = true
			sink.missingField(lit, field, typ)
		},
		UnknownField: func(field *ast.FieldInit, name string, typ ir.Type) {
			*fired = true
			sink.unknownField(field, name, typ)
		},
		UninferableRecord: func(lit *ast.RecordLit) {
			*fired = true
			sink.uninferableRecord(lit)
		},
		UnknownRecordType: func(lit *ast.RecordLit, name string) {
			*fired = true
			sink.unknownRecordType(lit, name)
		},
		NotARecord: func(lit *ast.RecordLit, typ ir.Type) {
			*fired = true
			sink.notARecord(lit, typ)
		},
		BoundNotSatisfied: func(call *ast.CallExpr, typ, bound ir.Type) {
			*fired = true
			sink.boundNotSatisfied(call, typ, bound)
		},
		UninferableTypeParam: func(call *ast.CallExpr, name string) {
			*fired = true
			sink.uninferableTypeParam(call, name)
		},
		NoMethodOnUnboundedTypeVar: func(node ast.Node, method string) {
			*fired = true
			sink.noMethodOnUnboundedTypeVar(node, method)
		},
		UnknownStatic: func(call *ast.CallExpr, name, typ string) {
			*fired = true
			sink.unknownStatic(call, name, typ)
		},
		MapKeyNotComparable: func(lit *ast.CollectionLit, key, bound ir.Type) {
			*fired = true
			sink.mapKeyNotComparable(lit, key, bound)
		},
		// Streams (not findings): forwarded as-is, never flip *fired.
		Checked: func(e ast.Expr, want ir.Type) {
			sink.checked(e, want)
		},
		SolvedFuncLit: func(lit *ast.FuncLit, t *ir.Func) {
			sink.solvedFuncLit(lit, t)
		},
		ResolvedMethod: func(call *ast.CallExpr, m *ir.Method) {
			sink.resolvedMethod(call, m)
		},
		ResolvedStatic: func(call *ast.CallExpr, m *ir.Method) {
			sink.resolvedStatic(call, m)
		},
		ResolvedFunc: func(call *ast.CallExpr, fd *ast.FuncDecl) {
			sink.resolvedFunc(call, fd)
		},
		CallSubst: func(call *ast.CallExpr, subst map[string]ir.Type) {
			sink.callSubst(call, subst)
		},
	}
}

// typesList renders the receiver and argument types as "recv, arg, ..." for the
// invalid-operation diagnostic.
func typesList(recv ir.Type, args []ir.Type) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, recv.String())
	for _, a := range args {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
