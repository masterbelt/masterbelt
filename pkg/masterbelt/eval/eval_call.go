// This file holds the receiver/definition readings the interpreter's call
// dispatch shares: which definition a method table is read from, whether a
// definition's underlying primitive backs a value kind, and the enum
// comparison fold.
package eval

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// enumComparison folds one of an enum value's six comparison methods. Equality
// (eql/neq) compares member identity — the enum definition and the member index
// — which the no-duplicate-value rule makes equivalent to comparing the base
// values. Ordering (lt/lteq/gt/gteq) compares the base values: an integer base
// numerically, a string base lexicographically. The argument must be the same
// enum (the type checker has already required it); a mismatch or an unevaluable
// base value folds to nothing.
func enumComparison(recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 {
		return nil
	}
	other := args[0]
	if other.Kind != ir.ConstEnum || other.EnumDef != recv.EnumDef {
		return nil
	}
	switch name {
	case "eql":
		return ir.BoolConstant(recv.EnumIndex == other.EnumIndex)
	case "neq":
		return ir.BoolConstant(recv.EnumIndex != other.EnumIndex)
	}
	// The ordering comparisons read the members' base values.
	cmp, ok := compareEnumValues(recv.EnumValue(), other.EnumValue())
	if !ok {
		return nil
	}
	switch name {
	case "lt":
		return ir.BoolConstant(cmp < 0)
	case "lteq":
		return ir.BoolConstant(cmp <= 0)
	case "gt":
		return ir.BoolConstant(cmp > 0)
	case "gteq":
		return ir.BoolConstant(cmp >= 0)
	}
	return nil
}

// compareEnumValues compares two enum members' base values, returning the sign
// of the comparison and whether it could be made: integers compare numerically,
// strings lexicographically. Two values of differing or unsupported kinds (or a
// nil value) cannot be compared.
func compareEnumValues(a, b *ir.Constant) (int, bool) {
	if a == nil || b == nil || a.Kind != b.Kind {
		return 0, false
	}
	switch a.Kind {
	case ir.ConstInt:
		return a.Int.Cmp(b.Int), true
	case ir.ConstString:
		return strings.Compare(a.Str, b.Str), true
	default:
		return 0, false
	}
}

// defBacksKind reports whether a type definition's underlying primitive can hold
// a value of the given kind: a Level (= int8) backs an integer, a Locale (=
// string) backs a string. It walks the def's body to the underlying primitive
// and checks its native descriptor against the kind; a def with no underlying
// primitive (a record, a union, an unresolved body) backs no scalar kind. It is
// the guard that keeps a def read from an annotation from applying to a value of
// the wrong kind.
func defBacksKind(reg *builtin.Registry, def *ir.TypeDef, kind ir.ConstKind) bool {
	// A record-bodied def (a record type, or a nominal type over one) backs a
	// record value: a method or getter on a record receiver reaches its body
	// through the receiver's annotation, the same syntactic channel a primitive
	// uses, so value.name folds on a record exactly as it does on a wrapped int.
	if kind == ir.ConstRecord {
		return recordOf(&ir.Named{Def: def}) != nil
	}
	n := underlyingPrimitive(reg, def, map[*ir.TypeDef]bool{})
	if n == nil {
		return false
	}
	switch kind {
	case ir.ConstInt:
		return n.IsInteger()
	case ir.ConstBool:
		return n.Bool
	case ir.ConstString:
		return n.Str
	case ir.ConstDatetime:
		return n.Datetime
	case ir.ConstDuration:
		return n.Duration
	case ir.ConstError:
		return n.Err
	case ir.ConstNull:
		return n.Null
	default:
		return false
	}
}

// defBacksKind, for a collection: a nominal type whose underlying is a list or a
// map (a Bag = list<int>) backs a ConstCollection. A scalar def is handled by the
// primitive path above; this is the collection arm, so a conversion to such a
// type (Bag([...])) passes the folded collection through, exactly as Level(5)
// passes its integer through — which lets a method, and a for, fold on the value.
func defBacksKindCollection(def *ir.TypeDef) bool {
	return underlyingCollectionDef(def, map[*ir.TypeDef]bool{}) != nil
}

// underlyingCollectionDef returns the list/map definition a nominal type bottoms
// out at — a Bag (= list<int>) yields the list def — by following the chain of
// Named/App bodies. It reports nil for a type with no collection underlying.
func underlyingCollectionDef(def *ir.TypeDef, seen map[*ir.TypeDef]bool) *ir.TypeDef {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	switch body := def.Body.(type) {
	case *ir.App:
		if body.Def != nil && (body.Def.Name == "list" || body.Def.Name == "map") {
			return body.Def
		}
		return underlyingCollectionDef(body.Def, seen)
	case *ir.Named:
		return underlyingCollectionDef(body.Def, seen)
	default:
		return nil
	}
}

// underlyingPrimitive returns the native descriptor of the primitive a nominal
// type bottoms out at — a Level (= int8) yields the int8 descriptor — by
// following the chain of Named bodies to a Builtin. It reports nil for a type
// with no scalar underlying (a record, a union, an enum, an unresolved body) or
// a cyclic alias chain.
func underlyingPrimitive(reg *builtin.Registry, def *ir.TypeDef, seen map[*ir.TypeDef]bool) *builtin.NativeType {
	if def == nil || seen[def] {
		return nil
	}
	seen[def] = true
	switch body := def.Body.(type) {
	case *ir.Builtin:
		n, _ := reg.Native(body.Name)
		return n
	case *ir.Named:
		return underlyingPrimitive(reg, body.Def, seen)
	default:
		return nil
	}
}

// methodTableDef returns the definition behind a Named or App type — the def a
// method table is read from — resolving a Builtin name through the registry so a
// prelude collection (list, map) reached as a Builtin body still yields its def.
// Any other type form (a union, record, function, type variable) has no def.
func methodTableDef(reg *builtin.Registry, t ir.Type) *ir.TypeDef {
	switch t := t.(type) {
	case *ir.Named:
		return t.Def
	case *ir.App:
		return t.Def
	case *ir.Builtin:
		d, _ := reg.Lookup(t.Name)
		return d
	}
	return nil
}

// collectionTypeName is the prelude type name a folded collection binds its
// methods through when no receiver annotation supplies a def: a settled map binds
// through map, everything else (a list, or an unknown empty collection) through
// list — the same conservative default that keeps the mapness-independent methods
// (len, fold, count, ...) folding on an unknown empty collection.
func collectionTypeName(recv *ir.Constant) string {
	if recv.IsMap() {
		return "map"
	}
	return "list"
}

// recordOf unwraps a static type to the record it ultimately is: a record type
// directly, or a nominal type (or applied generic) whose definition's body is a
// record. It returns nil for any non-record type. A seen-free single-step unwrap
// suffices — a record annotation is at most one Named deep here (a field's type
// is its resolved annotation, and a nominal record def's body is the record).
func recordOf(t ir.Type) *ir.Record {
	switch t := t.(type) {
	case *ir.Record:
		return t
	case *ir.Named:
		if t.Def != nil {
			if r, ok := t.Def.Body.(*ir.Record); ok {
				return r
			}
		}
	case *ir.App:
		if t.Def != nil {
			if r, ok := t.Def.Body.(*ir.Record); ok {
				return r
			}
		}
	}
	return nil
}

// namedOf wraps a definition as its nominal type, or nil for a nil def — the
// bridge from the def channels (which return *ir.TypeDef) to the type recvType
// threads.
func namedOf(def *ir.TypeDef) ir.Type {
	if def == nil {
		return nil
	}
	return &ir.Named{Def: def}
}

// tagMatchesType reports whether a value's union tag denotes the same member as a
// resolved arm type — member identity: a nominal type by its definition, a
// builtin by its name. It is the confident dispatch's decision, the value-side
// twin of the type layer's sameType narrowed to the two forms a tag ever takes.
func tagMatchesType(tag, arm ir.Type) bool {
	switch tag := tag.(type) {
	case *ir.Named:
		a, ok := arm.(*ir.Named)
		return ok && a.Def == tag.Def
	case *ir.Builtin:
		a, ok := arm.(*ir.Builtin)
		return ok && a.Name == tag.Name
	default:
		return false
	}
}

// scalarMatchesBuiltin reports whether a folded value is of the builtin type
// named name — the scalar kinds keyed on the registry's native classification,
// so a new primitive added to the registry is matched without a hardcoded list.
func scalarMatchesBuiltin(reg *builtin.Registry, scrut *ir.Constant, name string) bool {
	n, ok := reg.Native(name)
	return ok && builtinBacksKind(n, scrut.Kind)
}

// builtinBacksKind reports whether a builtin's native descriptor can hold a value
// of the given kind — the scalar classification a conversion's pass-through and a
// match's scalar arm both read, keyed on the registry rather than a hardcoded
// list of primitive names.
func builtinBacksKind(n *builtin.NativeType, kind ir.ConstKind) bool {
	switch kind {
	case ir.ConstInt:
		return n.IsInteger()
	case ir.ConstBool:
		return n.Bool
	case ir.ConstString:
		return n.Str
	case ir.ConstDatetime:
		return n.Datetime
	case ir.ConstDuration:
		return n.Duration
	case ir.ConstError:
		return n.Err
	case ir.ConstNull:
		return n.Null
	default:
		return false
	}
}

// constEqual reports whether two folded constants are structurally equal — the
// equality a switch dispatches on and a map keys by. It is ir.ConstantsEqual,
// the single shared definition the semantic engine's early cutoff also uses.
func constEqual(a, b *ir.Constant) bool {
	return ir.ConstantsEqual(a, b)
}
