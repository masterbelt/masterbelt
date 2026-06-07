// Package lower turns the abstract syntax tree into the resolved IR value graph.
// It is the single AST-to-IR walk: literals become IR literals, collection
// literals recurse, and a method call (the form every operator desugars to)
// becomes an ir.Call. The forms whose lowering depends on context — a value
// name, the receiver self, a record field access, a conversion, the null literal
// — are delegated to a Binder, so a constant initializer and a method body are
// the same walk over two binders.
//
// Lowering reaches resolution and type-name facts only through the Binder, so it
// has no dependency on the semantic query engine or the type resolver: it is a
// pure function of the AST and the binder.
package lower

import (
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Binder lowers the context-specific leaf forms of an expression — value names,
// self, field access, conversions, the null literal — to IR values. The forms
// shared by every context (literals, collection literals, and method calls) are
// lowered by Value itself.
type Binder interface {
	// Leaf lowers a context-specific expression form, recursing through sub for
	// its sub-expressions. It returns nil when the form does not lower in this
	// context.
	Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value
	// EnterFunc returns the binder for a function literal's body, with the
	// literal's parameters bound (so they lower to ir.ParamRef) on top of this
	// binder's own scope.
	EnterFunc(params []*ast.ParamDef) Binder
}

// EnumExpecter is the optional capability a Binder advertises to lower a bare
// member (Common, rather than Rarity.Common) wherever the surrounding syntax
// names the enum it belongs to. It has two readings, both off the binder's
// syntactic scope (never the type query, so value lowering stays independent of
// typing):
//
//   - ExpectedEnum reports the enum a value expression's static type names — a
//     let-bound local, a parameter, or self — which becomes the expected type a
//     bare member resolves through. It serves a switch scrutinee, an assignment
//     target, and an operator/method receiver alike.
//   - AnnotationEnum reports the enum a written type annotation names, the
//     channel a let initializer's bare member resolves through (the let
//     counterpart of a const's annotationEnum).
//
// Either reading returns nil when it cannot name an enum, leaving the bare
// members unresolved (the qualified form always works).
type EnumExpecter interface {
	ExpectedEnum(scrutinee ast.Expr) *ir.TypeDef
	AnnotationEnum(t ast.TypeExpr) *ir.TypeDef
}

// LocalBinder is the optional capability a body binder advertises to lower the
// mutable block-locals a let introduces. A binder without it lowers a body that
// has no lets (a const initializer, a refinement predicate), so the let and
// assignment statements simply do not appear.
//
// LetLocal records name as a let local on top of this binder's scope and returns
// the extended binder (used for the statements that follow the let, within the
// same block) along with the binding's settled type — the annotation when one is
// written, otherwise the value's inferred type. Block scoping falls out of the
// recursion: a nested block lowers through Body with the binder in force at its
// "{", and the extension never escapes back to the enclosing block's loop.
type LocalBinder interface {
	LetLocal(name string, annotation ast.TypeExpr, value ast.Expr) (Binder, ir.Type)
}

// EnumMemberResolver resolves a bare member identifier against an enum
// definition to its IR value — the same resolution a const initializer's bare
// member uses; the identifier rides along as the value's Syntax anchor. The
// lower package has no enum machinery of its own, so the semantic layer
// supplies this when it constructs a body binder.
type EnumMemberResolver interface {
	EnumMember(def *ir.TypeDef, id *ast.Identifier) ir.Value
}

// MatchBinder is the optional capability a body binder advertises to lower a
// match arm: it resolves the arm's member type expression and binds the arm's
// binding name to that (narrowed) type for the arm body. The lower package has
// no type resolver of its own, so the semantic layer supplies this when it
// constructs a body binder — the same way it supplies the let and enum
// capabilities. A binder without it lowers an arm body in which the binding stays
// unresolved (a context that has no narrowing scope, e.g. a const initializer,
// which never carries a match).
type MatchBinder interface {
	// ArmType resolves a match arm's member type expression to its resolved type,
	// or ir.Invalid when it names no type. It is the resolver's universe lookup,
	// the same path an annotation takes.
	ArmType(t ast.TypeExpr) ir.Type
	// NarrowLocal records the arm's binding name at the narrowed arm type on top
	// of this binder's scope and returns the extended binder, so a reference to
	// the binding inside the arm body lowers to an ir.LocalRef of that type.
	NarrowLocal(name string, typ ir.Type) Binder
}

// ForBinder is the optional capability a body binder advertises to lower a for
// loop's variable: it resolves the loop variable's settled type from the iterated
// expression (the foldable's value type for an of-loop, its key type for an
// in-loop) and binds the name at that type for the loop body, so a reference to
// it inside the body lowers to an ir.LocalRef. The lower package has no foldable
// machinery of its own, so the semantic layer supplies this — the same way it
// supplies the let, enum, and match capabilities. A binder without it lowers a
// loop body in which the variable stays unresolved (a context that never carries
// a for in practice, e.g. a const initializer).
type ForBinder interface {
	// ForLocal records name as the loop variable on top of this binder's scope and
	// returns the extended binder (used for the loop body) along with the
	// variable's settled type — the value type when of is true, otherwise the key
	// type. An unfoldable iter yields ir.Invalid (the semantic layer reports
	// not_iterable).
	ForLocal(name string, iter ast.Expr, of bool) (Binder, ir.Type)
}

// Value lowers an expression to its resolved IR value. The shared forms are
// lowered here; the context-specific leaves go through b.Leaf.
func Value(e ast.Expr, b Binder) ir.Value {
	switch e := e.(type) {
	case *ast.IntLit:
		return &ir.IntLiteral{Text: e.Text, Syntax: e}
	case *ast.StringLit:
		return &ir.StringLiteral{Value: e.Value, Syntax: e}
	case *ast.BoolLit:
		return &ir.BoolLiteral{Value: e.Value, Syntax: e}
	case *ast.DatetimeLit:
		return &ir.DatetimeLiteral{Text: e.Text, Syntax: e}
	case *ast.DurationLit:
		return &ir.DurationLiteral{Text: e.Text, Syntax: e}
	case *ast.CollectionLit:
		return collectionValue(e, b)
	case *ast.RecordLit:
		return recordValue(e, b)
	case *ast.CallExpr:
		return callValue(e, b)
	case *ast.AwaitExpr:
		// await wraps its operand: it marks the suspension point, adding
		// nothing to the value.
		return &ir.Await{Value: Value(e.Value, b), Syntax: e}
	case *ast.TernaryExpr:
		// cond ? then : else: the three operands lower as ordinary values; the
		// choice between the branches is the runtime's (and the folder's), so the
		// node carries both.
		return &ir.Ternary{Cond: Value(e.Cond, b), Then: Value(e.Then, b), Else: Value(e.Else, b), Syntax: e}
	case *ast.RangeExpr:
		// lo..hi keeps its own node rather than lowering to a range(...) Conversion:
		// the direction and the half-open trim depend on the bound values, settled
		// by the fold. Both bounds lower as ordinary values.
		return &ir.RangeLit{Lower: Value(e.Lower, b), Upper: Value(e.Upper, b), HalfOpen: e.HalfOpen, Syntax: e}
	case *ast.FuncLit:
		// The body lowers in a binder that binds the literal's parameters; its
		// own parameter values are supplied at evaluation, not here.
		names := make([]string, len(e.Params))
		for i, p := range e.Params {
			names[i] = p.Name
		}
		return &ir.FuncLiteral{Params: names, Body: Body(e.Body, b.EnterFunc(e.Params)), Syntax: e}
	default:
		return b.Leaf(e, sub(b))
	}
}

// collectionValue lowers a collection literal: each entry's optional key and its
// value lower as ordinary values.
func collectionValue(e *ast.CollectionLit, b Binder) ir.Value {
	entries := make([]ir.CollectionEntry, len(e.Entries))
	for i, entry := range e.Entries {
		var key ir.Value
		if entry.Key != nil {
			key = Value(entry.Key, b)
		}
		entries[i] = ir.CollectionEntry{Key: key, Value: Value(entry.Value, b)}
	}
	return &ir.CollectionLiteral{Entries: entries, Syntax: e}
}

// recordValue lowers a record literal: each field's value lowers as an ordinary
// value under the record's named type.
func recordValue(e *ast.RecordLit, b Binder) ir.Value {
	fields := make([]ir.RecordField, len(e.Fields))
	for i, f := range e.Fields {
		fields[i] = ir.RecordField{Name: f.Name, Value: Value(f.Value, b)}
	}
	return &ir.RecordValue{TypeName: e.TypeName, Fields: fields, Syntax: e}
}

// callValue lowers a call expression. The binder claims the context-specific
// call forms first — a call of a top-level function (by name, or through a
// namespace import), a conversion. What remains of the member-callee form is a
// method call; a callee that itself lowers to a value applies; any other callee
// lowers to nothing.
func callValue(e *ast.CallExpr, b Binder) ir.Value {
	if v := b.Leaf(e, sub(b)); v != nil {
		return v
	}
	if member, ok := e.Callee.(*ast.MemberExpr); ok {
		// A bare member as an operator/method argument resolves through the
		// receiver's static enum (rarity == Legend, desugared to
		// rarity.eql(Legend)): when the receiver syntactically names an enum,
		// the argument identifiers expect that enum. The enum's comparison
		// operators all take other: self, so the expectation matches; a
		// non-member name falls through the wrapper, and a non-enum receiver
		// invents no expectation.
		argBinder := expectEnum(b, expectedEnumOf(b, member.Receiver))
		args := make([]ir.Value, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = Value(a, argBinder)
		}
		return &ir.Call{Receiver: Value(member.Receiver, b), Method: member.Member.Name, Args: args, Syntax: e}
	}
	// A callee that itself lowers to a value applies — a function-typed
	// parameter (pred(value)), a local or constant bound to a fn value, an
	// immediately applied literal — mirroring the checker's function-value
	// arm. A callee that lowers to nothing (an unresolved name) lowers the
	// whole call to nothing, as before.
	if callee := Value(e.Callee, b); callee != nil {
		args := make([]ir.Value, len(e.Arguments))
		for i, a := range e.Arguments {
			args[i] = Value(a, b)
		}
		return &ir.Apply{Callee: callee, Args: args, Syntax: e}
	}
	return nil
}

// Body lowers a method body to its IR statements (nil for an extern or empty
// body), lowering each statement's expression through b.
//
// A let extends the binder for the statements that follow it in the same block
// (so a later reference to the local lowers to an ir.LocalRef), threading the
// extended binder forward — block scoping then falls out of the recursion: a
// nested if/switch lowers through Body with the binder in force at its block, and
// the extension does not leak back here. A binder that is not a LocalBinder (a
// const initializer) cannot carry lets, so its let/assign statements are skipped.
func Body(body []ast.Stmt, b Binder) []ir.Stmt {
	var stmts []ir.Stmt
	for _, s := range body {
		switch s := s.(type) {
		case *ast.ReturnStmt:
			stmts = append(stmts, &ir.Return{Value: Value(s.Value, b)})
		case *ast.ExprStmt:
			stmts = append(stmts, &ir.ExprStmt{Value: Value(s.X, b)})
		case *ast.LetStmt:
			lb, ok := b.(LocalBinder)
			if !ok {
				continue // a context that cannot carry lets (a const initializer)
			}
			// An annotated let resolves a bare member through its annotation's enum
			// (let r: Rarity = Legend), the body twin of a const initializer's rule.
			// The initializer sees the scope before the let, expecting that enum.
			value := Value(s.Value, expectEnum(b, annotationEnumOf(b, s.Type)))
			next, typ := lb.LetLocal(s.Name, s.Type, s.Value)
			stmts = append(stmts, &ir.Let{Name: s.Name, Type: typ, Value: value})
			b = next
		case *ast.AssignStmt:
			if _, ok := b.(LocalBinder); !ok {
				continue
			}
			stmts = append(stmts, assignStmt(s, b))
		case *ast.SwitchStmt:
			stmts = append(stmts, switchStmt(s, b))
		case *ast.MatchStmt:
			stmts = append(stmts, matchStmt(s, b))
		case *ast.IfStmt:
			stmts = append(stmts, ifStmt(s, b))
		case *ast.ForStmt:
			stmts = append(stmts, forStmt(s, b))
		default:
			panic(ast.UnhandledStmt(s))
		}
	}
	return stmts
}

// assignStmt lowers a reassignment. A plain identifier target names the let local
// being updated, rebound to the new value. A property write — a member access on
// a let local, p.name = v — rebinds that same local to a setter call:
// p = p.name(v) tagged Setter, the let-local rebinding semantics E-18's indexed
// write takes. The setter form is built whenever the target is a member access on
// an identifier; whether name actually names a setter (rather than a field) is
// the type-knowing layers' decision — the checker reports immutable_data when it
// is not one, and the folder applies the setter only when the receiver's def
// declares it. A non-identifier or chained target has no local name; it lowers
// with an empty name, which the semantic layer has reported as immutable-data, so
// the IR carries the (unreachable-at-runtime) value without a target.
func assignStmt(s *ast.AssignStmt, b Binder) *ir.Assign {
	if m, ok := s.Target.(*ast.MemberExpr); ok {
		if recv, ok := m.Receiver.(*ast.Identifier); ok {
			return &ir.Assign{Name: recv.Name, Value: &ir.Call{
				Receiver: Value(m.Receiver, b),
				Method:   m.Member.Name,
				Args:     []ir.Value{Value(s.Value, b)},
				Setter:   true,
			}}
		}
		return &ir.Assign{Name: "", Value: Value(s.Value, b)}
	}
	name := ""
	if id, ok := s.Target.(*ast.Identifier); ok {
		name = id.Name
	}
	// A bare member on the right resolves through the target local's enum (r =
	// Common, where r is a Rarity let), the static type read syntactically from
	// the binder's scope — the assignment twin of the let-initializer rule.
	return &ir.Assign{Name: name, Value: Value(s.Value, expectEnum(b, expectedEnumOf(b, s.Target)))}
}

// ifStmt lowers an if statement: its condition, its then body, and its else
// branch — an else-if chain into a nested ir.If, a plain else into the Else
// body. An if yields no value, so only its condition and branch bodies lower.
func ifStmt(s *ast.IfStmt, b Binder) *ir.If {
	out := &ir.If{Cond: Value(s.Cond, b), Then: Body(s.Then, b)}
	if s.ElseIf != nil {
		out.ElseIf = ifStmt(s.ElseIf, b)
	}
	if s.Else != nil {
		out.Else = Body(s.Else, b)
	}
	return out
}

// forStmt lowers a for statement: its iterated collection, its loop variable
// (bound at its settled element type through the binder's ForBinder capability,
// so a reference to it inside the body lowers to an ir.LocalRef), and its body
// lowered in that extended scope. A binder without the capability (no for scope)
// lowers the body with the variable unresolved — a context that never carries a
// for in practice. A for yields no value, so only its iter and body lower.
func forStmt(s *ast.ForStmt, b Binder) *ir.For {
	of := s.Kind == ast.ForOf
	out := &ir.For{Var: s.Var, Of: of, Iter: Value(s.Iter, b)}
	bodyBinder := b
	if fb, ok := b.(ForBinder); ok && s.Var != "" {
		next, typ := fb.ForLocal(s.Var, s.Iter, of)
		out.VarType = typ
		bodyBinder = next
	}
	out.Body = Body(s.Body, bodyBinder)
	return out
}

// switchStmt lowers a switch statement: its scrutinee, each arm's value
// patterns and body, and the wildcard Else body. An arm value lowers as an
// ordinary expression, with the scrutinee's enum (when the binder can name one)
// reached so a bare member resolves — the same path a const initializer's bare
// member takes.
func switchStmt(s *ast.SwitchStmt, b Binder) *ir.Switch {
	sw := &ir.Switch{Scrutinee: Value(s.Scrutinee, b)}
	armValue := expectEnum(b, expectedEnumOf(b, s.Scrutinee))
	for _, arm := range s.Arms {
		values := make([]ir.Value, len(arm.Values))
		for i, v := range arm.Values {
			values[i] = Value(v, armValue)
		}
		sw.Arms = append(sw.Arms, ir.SwitchArm{Values: values, Body: Body(arm.Body, b)})
	}
	if s.Else != nil {
		// An empty wildcard body still distinguishes a switch with a catch-all
		// from one without, so the Else slice is non-nil even when empty.
		if body := Body(s.Else, b); body != nil {
			sw.Else = body
		} else {
			sw.Else = []ir.Stmt{}
		}
	}
	return sw
}

// matchStmt lowers a match statement: its scrutinee, each arm's resolved member
// type, its narrowed binding, and its body, and the wildcard Else body. The arm
// body is lowered with the binding bound to the narrowed arm type (through the
// binder's MatchBinder capability), so a reference to it inside the body lowers
// to an ir.LocalRef the type checker has narrowed. A binder without that
// capability (no narrowing scope) lowers the arm body unchanged, leaving the
// binding unresolved — a context that never carries a match in practice.
func matchStmt(s *ast.MatchStmt, b Binder) *ir.Match {
	m := &ir.Match{Scrutinee: Value(s.Scrutinee, b)}
	mb, hasNarrow := b.(MatchBinder)
	for _, arm := range s.Arms {
		armType := ir.Invalid
		armBinder := b
		if hasNarrow {
			armType = mb.ArmType(arm.Type)
			if arm.Bind != "" {
				armBinder = mb.NarrowLocal(arm.Bind, armType)
			}
		}
		m.Arms = append(m.Arms, ir.MatchArm{Type: armType, Name: arm.Bind, Body: Body(arm.Body, armBinder)})
	}
	if s.Else != nil {
		// An empty wildcard body still distinguishes a match with a catch-all
		// from one without, so the Else slice is non-nil even when empty.
		if body := Body(s.Else, b); body != nil {
			m.Else = body
		} else {
			m.Else = []ir.Stmt{}
		}
	}
	return m
}

// expectEnum returns the binder a bare member resolves through under a def
// expectation: b wrapped so a leaf identifier naming a member of def lowers to
// that member's value, or b unchanged when there is no enum to expect (def is
// nil) or the binder cannot resolve members (a const initializer with no
// resolver). It is the one wrapping point the let-initializer, assignment, and
// operator-argument channels share, with the switch arm.
func expectEnum(b Binder, def *ir.TypeDef) Binder {
	if def == nil {
		return b
	}
	res, ok := b.(EnumMemberResolver)
	if !ok {
		return b
	}
	return expectingEnum{Binder: b, def: def, res: res}
}

// expectedEnumOf returns the enum a value expression's static type names through
// the binder's EnumExpecter, or nil when the binder cannot answer (it is not an
// EnumExpecter, or names no enum for e). It is how the assignment target and the
// operator/method receiver read their expected enum, the same reading a switch
// scrutinee uses.
func expectedEnumOf(b Binder, e ast.Expr) *ir.TypeDef {
	if exp, ok := b.(EnumExpecter); ok {
		return exp.ExpectedEnum(e)
	}
	return nil
}

// annotationEnumOf returns the enum a written type annotation names through the
// binder's EnumExpecter, or nil when the binder cannot answer. It is how a let
// initializer reads the enum its bare member resolves through.
func annotationEnumOf(b Binder, t ast.TypeExpr) *ir.TypeDef {
	if exp, ok := b.(EnumExpecter); ok {
		return exp.AnnotationEnum(t)
	}
	return nil
}

// expectingEnum wraps a Binder so a bare identifier that names a member of def
// lowers to that member's value, while every other leaf falls through to the
// wrapped binder. It is the body counterpart of the const binder's expected-enum
// rule, shared by a switch arm's value patterns, a let initializer, an
// assignment's right-hand side, and an operator/method argument.
type expectingEnum struct {
	Binder
	def *ir.TypeDef
	res EnumMemberResolver
}

func (b expectingEnum) Leaf(e ast.Expr, sub func(ast.Expr) ir.Value) ir.Value {
	if v := b.Binder.Leaf(e, sub); v != nil {
		return v
	}
	if id, ok := e.(*ast.Identifier); ok {
		if v := b.res.EnumMember(b.def, id); v != nil {
			return v
		}
	}
	return nil
}

// sub returns the sub-expression lowering a Binder.Leaf recurses through: Value
// bound to the same binder.
func sub(b Binder) func(ast.Expr) ir.Value {
	return func(e ast.Expr) ir.Value { return Value(e, b) }
}
