// This file holds the concrete typing scopes the inference and checking walks
// run in: funcScope for a function literal's body, constScope for a constant
// initializer, and BodyScope for a method or function body. Each implements the
// scope interface — supplying the registry, the type universe, and the rules
// for the context-specific leaf forms — so the one walk (exprType / check) sees
// the right meaning for self, a name, a conversion, and a function call in
// whatever context it is invoked.

package infer

import (
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// funcScope types a function literal's body: the literal's own parameters
// resolve to their (declared or pushed-down) types, and every other leaf is
// delegated to the enclosing scope — mirroring how funcBinder chains the
// lowering scopes for the same body. A nested literal just wraps another
// funcScope around this one.
//
// A lambda block body may also introduce let-bound block-locals; locals carries
// each one in scope to its settled type, read before a parameter (a let shadows
// a same-named parameter) and the body counterpart of params. It is nil in a
// body with no lets — the common arrow/return form — and grows as the body walk
// descends a block's lets, exactly as BodyScope.Locals does for a method body.
type funcScope struct {
	outer  scope
	params map[string]ir.Type
	locals map[string]ir.Type
}

// withLocal returns a copy of the scope with name bound to typ on top of its
// locals — used as a body walk descends past a let, so the statements after it
// (within the same block) resolve the new local. The map is copied so the
// binding never escapes back to an enclosing block.
func (s funcScope) withLocal(name string, typ ir.Type) funcScope {
	locals := make(map[string]ir.Type, len(s.locals)+1)
	for k, v := range s.locals {
		locals[k] = v
	}
	locals[name] = typ
	s.locals = locals
	return s
}

func (s funcScope) registry() *builtin.Registry { return s.outer.registry() }

// self inherits the enclosing context's receiver: a function literal in a
// method body keeps self, exactly as the lowering's funcBinder delegates its
// leaves outward.
func (s funcScope) self() ir.Type { return s.outer.self() }

// rigid inherits the enclosing context's type-parameter scope: a literal in a
// generic body sees the same rigid variables its body does.
func (s funcScope) rigid(name string) bool { return s.outer.rigid(name) }

// tscope inherits the enclosing context's generic type-parameter scope: a
// literal's annotations (its parameters, its result, a let or match arm in its
// body) resolve the enclosing generic's T exactly as the body's own do.
func (s funcScope) tscope() TypeScope { return s.outer.tscope() }

func (s funcScope) universe() map[string]*ir.TypeDef { return s.outer.universe() }

func (s funcScope) qualified() func(namespace, name string) *ir.TypeDef { return s.outer.qualified() }

// shadows reports whether name is bound by a let local or a parameter of this
// literal — either shadows a same-named type or top-level function (and so a
// conversion or function call) reached through the enclosing scope.
func (s funcScope) shadows(name string) bool {
	if _, ok := s.locals[name]; ok {
		return true
	}
	_, ok := s.params[name]
	return ok
}

func (s funcScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.Identifier:
		// A let-bound local shadows a same-named parameter, so it is read first.
		if t, ok := s.locals[e.Name]; ok {
			return t
		}
		if t, ok := s.params[e.Name]; ok {
			return t
		}
	case *ast.MemberExpr:
		// A member access whose receiver is this lambda's parameter or local is a
		// field or getter read on that binding, not a type-member read on a
		// same-named type: the binding shadows the type, so the receiver is a value.
		if recv, ok := e.Receiver.(*ast.Identifier); ok && s.shadows(recv.Name) {
			return memberReadType(s.registry(), s.leaf(recv), e.Member.Name)
		}
	}
	return s.outer.leaf(e)
}

func (s funcScope) conv(id *ast.Identifier) ir.Type {
	if s.shadows(id.Name) {
		return ir.Invalid // a local or parameter shadows a same-named type
	}
	return s.outer.conv(id)
}

func (s funcScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if s.shadows(id.Name) {
		return nil // a local or parameter shadows a same-named function
	}
	return s.outer.fn(id)
}

func (s funcScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	if recv, ok := m.Receiver.(*ast.Identifier); ok {
		if s.shadows(recv.Name) {
			return nil // a local or parameter shadows a same-named namespace
		}
	}
	return s.outer.fnMember(m)
}

func (s funcScope) nsReceiver(recv ast.Expr) bool {
	if id, ok := recv.(*ast.Identifier); ok && s.shadows(id.Name) {
		return false // a lambda local or parameter shadows the namespace import
	}
	return s.outer.nsReceiver(recv)
}

// metatype is the type of a reified type value — the builtin `type` (type :
// type), the type a bare type name carries in value position. It is built fresh
// per call, like the other primitive types the scopes synthesize.
func metatype() ir.Type { return &ir.Builtin{Name: builtin.NameType} }

// constScope types a constant initializer: the context-specific forms are a
// value reference, whose type is its referent's, a conversion, whose type
// is the type it names, and the null literal. A field access reads a
// record-typed constant's field (Hero.lv); self is not meaningful in a
// constant, so it is ir.Invalid.
type constScope struct{ env Env }

func (s constScope) registry() *builtin.Registry { return s.env.Registry() }

// self: a constant initializer has no receiver.
func (s constScope) self() ir.Type { return ir.Invalid }

// rigid: a constant initializer has no generic type parameters in scope.
func (s constScope) rigid(string) bool { return false }

// tscope: a constant initializer has no generic type parameters, so a lambda
// in one resolves its annotations with no type-variable scope.
func (s constScope) tscope() TypeScope { return nil }

func (s constScope) universe() map[string]*ir.TypeDef { return s.env.Universe() }

func (s constScope) qualified() func(namespace, name string) *ir.TypeDef { return s.env.QualifiedType }

// valueShadows reports whether a namespace identifier is shadowed by a value —
// a constant of the same name — so a qualified type projection (geo.Item.id)
// defers to the fields of a const named geo.
func (s constScope) valueShadows(id *ast.Identifier) bool {
	return s.env.Resolve(id) != nil
}

func (s constScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.NullLit:
		return &ir.Builtin{Name: "null"}
	case *ast.Identifier:
		if target := s.env.Resolve(e); target != nil {
			return s.env.TypeOf(target)
		}
		// A bare type name in value position is a compile-time type value, of type
		// `type` (the metatype): const x = int8. A value of that name (a constant)
		// wins above, so only a name resolving to a type alone reaches here. (A
		// master is its relation only in a body, where the query driver can run it
		// against data; a const cannot evaluate a relation, so it stays a type value.)
		if _, ok := s.universe()[e.Name]; ok {
			return metatype()
		}
	case *ast.MemberExpr:
		// A member access on a namespace import (geo.Origin) inherits the
		// referenced declaration's type.
		if target := s.env.ResolveMember(e); target != nil {
			return s.env.TypeOf(target)
		}
		// A member access whose receiver names a type — an enum member
		// (Rarity.Common), an associated constant (sbyte.Max, Level.Max), or a field
		// projected off a local or namespace-qualified type (Item.id, geo.Item.id) —
		// is a value of that type, resolved through the single member resolver.
		if t := typeMemberType(s.registry(), s.universe(), s.qualified(), s.valueShadows, e); t != ir.Invalid {
			return t
		}
		// Otherwise the receiver is a value: a field access on a record-typed
		// constant (Hero.lv, and a nested path a.b.c) or a getter read
		// (Freezing.fahrenheit). The receiver types through the same const scope,
		// so it reaches another constant's record value (or this one's,
		// recursively).
		return memberReadType(s.registry(), exprType(e.Receiver, s), e.Member.Name)
	}
	return ir.Invalid
}

func (s constScope) conv(id *ast.Identifier) ir.Type {
	if d, ok := s.env.Universe()[id.Name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: id.Name}
		}
		return &ir.Named{Def: d}
	}
	return ir.Invalid
}

func (s constScope) fn(id *ast.Identifier) []*ast.FuncDecl { return s.env.ResolveFunc(id) }

func (s constScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	return s.env.ResolveFuncMember(m)
}

// nsReceiver: a constant initializer has no generic type parameters, so it never
// reports a type parameter in value position; the value-walk shortcut a namespace
// receiver takes is irrelevant here, and the namespace member read keeps its
// existing reading.
func (s constScope) nsReceiver(ast.Expr) bool { return false }

// BodyScope types a method or function body: the receiver type (Self —
// ir.Invalid in a function, which has none), the parameter types (Params),
// the type universe (Universe) a conversion resolves against, and the
// top-level functions callable from the body (Funcs), alongside the registry.
type BodyScope struct {
	Reg      *builtin.Registry
	Universe map[string]*ir.TypeDef
	// Qualified is the namespace-qualified type lookup the body's annotations
	// and conversions resolve through, or nil when no namespaces are in scope.
	Qualified func(namespace, name string) *ir.TypeDef
	Self      ir.Type
	Params    map[string]ir.Type
	// TScope is the generic type-parameter scope in effect in the body — the
	// enclosing function's or method's type parameters, each mapped to its bound
	// (nil if unbounded). A type annotation in the body (a let, a match/switch arm
	// type) resolves a name through it, so a parameter T may be written there and
	// resolves to a TypeVar carrying its bound rather than an unknown type. It is
	// nil in a body with no type parameters (and in a refinement predicate).
	TScope TypeScope
	// Locals maps each let-bound block-local in scope to its settled type. A
	// reference to one resolves to that type, shadowing a same-named parameter or
	// type; it is nil in a body with no lets (and in a refinement predicate, which
	// has none). The checking walk grows it as it descends a block's lets.
	Locals map[string]ir.Type
	// Funcs is the file's top-level functions by name — each name carrying
	// its overload set in source order — or nil when none are in scope (a
	// refinement predicate).
	Funcs map[string][]*ast.FuncDecl
	// ConstShadows reports whether a name is bound by a top-level constant — a
	// value that shadows a same-named namespace import in value position, so a
	// qualified member off it (geo.Item) is read as a field of the const rather
	// than reified as the imported type. It is the body's reach to the constant
	// surface BodyScope does not otherwise carry (locals and params it does); nil
	// where there is none (a refinement predicate), leaving only locals and params
	// to shadow. The const initializer's resolve check is the const-position twin.
	ConstShadows func(*ast.Identifier) bool
	// QualifiedFuncs is the namespace-qualified function lookup
	// (geo.area -> the target's exported overload set), or nil when no
	// namespaces are in scope.
	QualifiedFuncs func(namespace, name string) []*ast.FuncDecl
	// NamespaceShadows reports whether a name is a namespace import in the file, so
	// a member read off it (T.x reading the import's exported member) is a namespace
	// access whose receiver is not a value — not a value-position use of a same-named
	// type parameter. It is nil where no namespaces are in scope.
	NamespaceShadows func(*ast.Identifier) bool
	// ReportTypeParamValue reports a generic type parameter consumed as a value —
	// a bare T (T == string, return T) or the receiver of a value member read T.x —
	// at the value leaf where the name resolves to nothing but a type parameter:
	// not a local, parameter, or constant, each of which the leaf resolves first
	// and so shadows it by construction. The one value-position shadow the leaf
	// does not itself carry, a namespace import, is filtered by the reporter, which
	// holds the file's import set. It is the value leaf's reach to the diagnostic
	// surface BodyScope does not otherwise carry; nil in a non-reporting walk (the
	// sink-only func-literal settling pass, the effect and refinement walks),
	// leaving the use to its prior silent reading.
	ReportTypeParamValue func(node ast.Node, name string)
	// Relation marks a validate all clause's scope, where the subject is the
	// master's relation rather than a row: a bare count there types as the
	// relation's row count, the per-table counterpart of self omission. It is false
	// everywhere else, so count keeps its ordinary readings (a local, a parameter)
	// outside an all clause.
	Relation bool
}

// RelationCountName is the bare name a validate all clause reads as its relation's
// row count — the per-table aggregate, resolved last-resort like a self member so a
// local, parameter, type, or constant of the same name shadows it.
const RelationCountName = "count"

// RelationCountType is the type a relation count yields: an arbitrary-precision
// integer, since a table's row count carries no fixed width. The lowering stamps
// the RelationCount node with it and the checker types the bare count to it, so
// the two agree.
func RelationCountType() ir.Type { return &ir.Builtin{Name: "nint"} }

// RelationType builds relation<master> — the relation algebra's relation of a
// master's rows, the type a master in value position has (the value a bare master
// name denotes, on which the query operations are methods). It is ir.Invalid when
// the registry has no relation type (a degraded prelude).
func RelationType(reg *builtin.Registry, master *ir.TypeDef) ir.Type {
	def, ok := reg.Lookup(builtin.NameRelation)
	if !ok {
		return ir.Invalid
	}
	return &ir.App{Def: def, Args: []ir.Type{&ir.Named{Def: master}}}
}

// isRelationType reports whether t is a relation<M> — an application of the
// relation builtin. It is the value-reading test the static-call path uses to tell
// an unshadowed master name (which reads as its relation) from a master shadowed by
// a constant (which reads as the constant, not the relation).
func isRelationType(reg *builtin.Registry, t ir.Type) bool {
	app, ok := t.(*ir.App)
	if !ok {
		return false
	}
	def, ok := reg.Lookup(builtin.NameRelation)
	return ok && app.Def == def
}

// Body infers the type of a method-body expression: self, a parameter, a
// literal, a record field access, a type conversion (T(x)), or a method call
// (the form operators desugar to). An unresolvable expression is ir.Invalid.
func Body(e ast.Expr, s BodyScope) ir.Type { return exprType(e, s) }

func (s BodyScope) registry() *builtin.Registry { return s.Reg }

// self is the receiver type — ir.Invalid in a function or static-fn body,
// which the implicit self-call claim then skips.
func (s BodyScope) self() ir.Type {
	if s.Self == nil {
		return ir.Invalid
	}
	return s.Self
}

// rigid reports whether name is a generic type parameter of the enclosing
// declaration (TScope) — the enclosing type's or function's own parameters, a
// known type within the body rather than an inference hole.
func (s BodyScope) rigid(name string) bool {
	_, ok := s.TScope[name]
	return ok
}

// tscope is the body's generic type-parameter scope — the enclosing function's
// or method's type parameters — that a body annotation (a let, a match arm) and
// a lambda within it resolve a written T through.
func (s BodyScope) tscope() TypeScope { return s.TScope }

func (s BodyScope) universe() map[string]*ir.TypeDef { return s.Universe }

func (s BodyScope) qualified() func(namespace, name string) *ir.TypeDef { return s.Qualified }

// shadows reports whether name is bound by a let local or a parameter — either
// shadows a same-named type or top-level function in value position.
func (s BodyScope) shadows(name string) bool {
	if _, ok := s.Locals[name]; ok {
		return true
	}
	_, isParam := s.Params[name]
	return isParam
}

func (s BodyScope) conv(id *ast.Identifier) ir.Type {
	if s.shadows(id.Name) {
		return ir.Invalid // a local or parameter shadows a same-named type
	}
	// A generic type parameter names a type in conversion position (T(x)): it
	// resolves to the type variable carrying its bound, the same as the lowering's
	// ResolveName does, and shadows a same-named declared type or top-level function
	// — so the conversion is a type position, typed as the parameter, its validity
	// deferred to the call site that instantiates T. A value of the same name is
	// excluded above (it shadows the type parameter and takes the function-value
	// path).
	if bound, ok := s.TScope[id.Name]; ok {
		return &ir.TypeVar{Name: id.Name, Bound: bound}
	}
	return s.lookupType(id.Name)
}

func (s BodyScope) fn(id *ast.Identifier) []*ast.FuncDecl {
	if s.shadows(id.Name) {
		return nil // a local or parameter shadows a same-named function
	}
	return s.Funcs[id.Name]
}

func (s BodyScope) fnMember(m *ast.MemberExpr) []*ast.FuncDecl {
	recv, ok := m.Receiver.(*ast.Identifier)
	if !ok || s.QualifiedFuncs == nil {
		return nil
	}
	if s.shadows(recv.Name) {
		return nil // a local or parameter shadows a same-named namespace
	}
	return s.QualifiedFuncs(recv.Name, m.Member.Name)
}

func (s BodyScope) nsReceiver(recv ast.Expr) bool {
	id, ok := recv.(*ast.Identifier)
	if !ok || s.shadows(id.Name) {
		return false // a local or parameter shadows the namespace import
	}
	return s.NamespaceShadows != nil && s.NamespaceShadows(id)
}

func (s BodyScope) leaf(e ast.Expr) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		return s.Self
	case *ast.NullLit:
		return &ir.Builtin{Name: "null"}
	case *ast.Identifier:
		return s.identifierLeaf(e)
	case *ast.MemberExpr:
		// A member access whose receiver names a type — an enum member
		// (Element.Fire) or an associated constant (int8.Max, Level.Max) — is a
		// value of that type; a local or parameter shadowing the type name takes
		// the record-field reading instead. A receiver that also reads a self member
		// (self omission is last resort) only takes the self.recv.x reading below
		// when the type member does not resolve here.
		if t, ok := s.typeMemberValue(e); ok {
			return t
		}
		// A namespace-import receiver (T.x reading the import's exported member) is
		// not a value: the receiver is left unread rather than taken as one, which
		// would misreport a same-named type parameter as used in value position. The
		// member read itself is unresolved here, as it was — the import's value
		// surface is not carried by the body scope.
		if s.nsReceiver(e.Receiver) {
			return ir.Invalid
		}
		// A member access used as a value is a record field access or a getter read
		// (value.name) the receiver's type declares.
		return memberReadType(s.Reg, exprType(e.Receiver, s), e.Member.Name)
	default:
		return ir.Invalid
	}
}

// identifierLeaf types a bare name in value position. The ordinary scope is read
// first — a let local, a parameter, a type parameter, a type name — and a
// readable member of self (a field or getter) is the last resort: a bare name
// reads self.X only where it resolves no other way (self omission). This makes
// the feature purely additive — a name that already resolves keeps its meaning,
// and a name that collides with a type, parameter, or local takes that reading,
// not the self member.
func (s BodyScope) identifierLeaf(e *ast.Identifier) ir.Type {
	// A let-bound local shadows a same-named parameter, so it is read first.
	if t, ok := s.Locals[e.Name]; ok {
		return t
	}
	if t, ok := s.Params[e.Name]; ok {
		return t
	}
	// A generic type parameter consumed as a value (T == string, return T, or the
	// receiver of a value read T.x reached through exprType below) is a compile-
	// time type, not a foldable value: it is rejected, and reported in a reporting
	// walk. A top-level constant of the same name shadows it (the body reads the
	// constant), so the constant case is excluded; a namespace import — the one
	// value-position shadow this scope does not carry — is excluded upstream, where
	// a namespace member read T.x resolves before its receiver is taken as a value.
	if s.rigid(e.Name) && (s.ConstShadows == nil || !s.ConstShadows(e)) {
		if s.ReportTypeParamValue != nil {
			s.ReportTypeParamValue(e, e.Name)
		}
		return ir.Invalid
	}
	// A bare master name in value position is its relation — the set of all its
	// rows, on which the query operations (where, count) are methods. Every other
	// bare type name in value position is a compile-time type value of the metatype
	// `type` — the same reading the constant scope gives it. A top-level constant of
	// the same name shadows the master (the lowering resolves the constant first), so
	// the relation reading applies only when no constant shadows the name.
	if def, ok := s.Universe[e.Name]; ok {
		if def.Master != nil && (s.ConstShadows == nil || !s.ConstShadows(e)) {
			return RelationType(s.Reg, def)
		}
		if def.Master == nil {
			return metatype()
		}
	}
	// A top-level constant of the same name resolves through the constant scope —
	// the lowering reads it through constRef, before the implicit-self fallback —
	// so self omission, being last resort, does not read self.name over it. Left
	// ir.Invalid the way a bare constant reference is here (the write-back types it
	// from the constant), so the checker and lowering agree the name is the constant.
	if s.ConstShadows != nil && s.ConstShadows(e) {
		return ir.Invalid
	}
	// Last resort: a bare name that resolves no other way reads a readable member
	// of self (self omitted) — power means self.power only where power is not a
	// local, parameter, type parameter, type, or constant. Typed exactly as the
	// explicit self.power member read is.
	if s.Self != nil {
		if t := memberReadType(s.registry(), s.Self, e.Name); t != ir.Invalid {
			return t
		}
	}
	// In a validate all clause the subject is the relation, not a row: a bare count
	// that resolves no other way types as the relation's row count, the same
	// last-resort rule as self omission above.
	if s.Relation && e.Name == RelationCountName {
		return RelationCountType()
	}
	return ir.Invalid
}

// typeMemberValue reads a member access whose receiver names a type as a value:
// an enum member (Element.Fire) or an associated constant (int8.Max, Level.Max),
// of that type. It returns ok=false — leaving the caller to take the
// record-field reading — when the receiver does not name a type, a local or
// parameter shadows the type name, or it is neither member kind.
func (s BodyScope) typeMemberValue(e *ast.MemberExpr) (ir.Type, bool) {
	// A local or parameter shadowing a same-named type takes the record-field
	// reading instead; only a bare type-name receiver can be shadowed, never a
	// namespace-qualified one (geo.Item).
	if recv, ok := e.Receiver.(*ast.Identifier); ok && s.shadows(recv.Name) {
		return ir.Invalid, false
	}
	// A namespace-qualified master name in value position is its relation — the
	// qualified twin of the bare master name, on which where and count are methods.
	// The bare reading is identifierLeaf's; this is the imported one.
	if def := QualifiedTypeDef(s.Qualified, s.valueShadows, e); def != nil && def.Master != nil {
		return RelationType(s.Reg, def), true
	}
	if t := typeMemberType(s.Reg, s.Universe, s.Qualified, s.valueShadows, e); t != ir.Invalid {
		return t, true
	}
	return ir.Invalid, false
}

// valueShadows reports whether a namespace identifier is shadowed by a value in
// scope — a let-bound local, a parameter, or a top-level constant — so a
// qualified type member (geo.Item.id, or the bare geo.Item type value) defers to
// a value receiver named geo. It is the body twin of the const initializer's
// resolve check, which catches a same-named const the same way.
func (s BodyScope) valueShadows(id *ast.Identifier) bool {
	if s.shadows(id.Name) {
		return true
	}
	return s.ConstShadows != nil && s.ConstShadows(id)
}

// lookupType resolves a type name (a conversion callee) to its type against
// the body's universe — which carries the prelude beneath the declared and
// imported types, so there is no second source to consult.
func (s BodyScope) lookupType(name string) ir.Type {
	if d, ok := s.Universe[name]; ok {
		if d.Builtin {
			return &ir.Builtin{Name: name}
		}
		return &ir.Named{Def: d}
	}
	return ir.Invalid
}
