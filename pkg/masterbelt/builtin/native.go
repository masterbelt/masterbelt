// This file holds the native descriptions of the primitives: the integer
// value-range kinds, the NativeType descriptor and its predicates, and the
// builders that assemble each primitive's extern operator-method signatures.

package builtin

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// IntKind describes an integer primitive's representation. Bits == 0 means
// arbitrary precision: a signed one then has no bounds, an unsigned one only a
// lower bound of zero.
type IntKind struct {
	Signed bool
	Bits   uint
}

// bounds returns the inclusive value range of the integer kind. A nil bound
// means "unbounded on that side".
func (k IntKind) bounds() (lo, hi *big.Int) {
	one := big.NewInt(1)
	if k.Bits == 0 {
		if k.Signed {
			return nil, nil
		}
		return big.NewInt(0), nil
	}
	if k.Signed {
		half := new(big.Int).Lsh(one, k.Bits-1)
		return new(big.Int).Neg(half), new(big.Int).Sub(half, one)
	}
	return big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(one, k.Bits), one)
}

// NativeType is the native description of a primitive: its numeric kind (for an
// integer), or a flag marking the boolean, string, null, datetime, duration, or
// error type.
type NativeType struct {
	Name     string
	Int      *IntKind // non-nil for an integer primitive
	Bool     bool     // the boolean type
	Str      bool     // the string type
	Null     bool     // the null type
	Datetime bool     // the datetime type (a UTC instant in epoch milliseconds)
	Duration bool     // the duration type (a span in milliseconds)
	Err      bool     // the error type (a recoverable failure carrying its message)
}

// IsInteger reports whether the primitive is an integer type.
func (n *NativeType) IsInteger() bool { return n.Int != nil }

// IsBoolean reports whether the primitive is the boolean type.
func (n *NativeType) IsBoolean() bool { return n.Bool }

// IsString reports whether the primitive is the string type.
func (n *NativeType) IsString() bool { return n.Str }

// Bounds returns the inclusive value range of an integer primitive: the lowest
// and highest representable value, with a nil bound meaning "unbounded on that
// side" (the arbitrary-precision signed int has neither; the arbitrary-
// precision unsigned int has only the lower bound of zero). A non-integer
// primitive has no range — both bounds are nil. It is the source the builtin
// associated constants Max/Min draw their value from.
func (n *NativeType) Bounds() (lo, hi *big.Int) {
	if n.Int == nil {
		return nil, nil
	}
	return n.Int.bounds()
}

// Fits reports whether v is within the primitive's value range. A non-integer
// primitive (or an arbitrary-precision integer) accepts any value within its
// (possibly half-open) range.
func (n *NativeType) Fits(v *big.Int) bool {
	if n.Int == nil {
		return true
	}
	lo, hi := n.Int.bounds()
	if lo != nil && v.Cmp(lo) < 0 {
		return false
	}
	if hi != nil && v.Cmp(hi) > 0 {
		return false
	}
	return true
}

// boolType is the shared boolean primitive type used in operator-method
// signatures (the result of the comparison and equality methods).
var boolType ir.Type = &ir.Builtin{Name: "bool"}

// stringType is the shared string primitive type used in method signatures
// (the result of error.message).
var stringType ir.Type = &ir.Builtin{Name: "string"}

func self() ir.Type { return &ir.SelfType{} }

// externMethod builds an extern operator-method signature: its parameter types
// and result type, with no body (the implementation is an Intrinsic).
func externMethod(name string, result ir.Type, params ...ir.Type) *ir.Method {
	ps := make([]ir.Param, len(params))
	for i, p := range params {
		ps[i] = ir.Param{Name: "other", Type: p}
	}
	return &ir.Method{Name: name, Public: true, Extern: true, Params: ps, Result: result}
}

// integerMethods is the operator-method signature set shared by every integer
// primitive: arithmetic returns self, comparisons and equality return bool, and
// the unary signs return self.
func integerMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("pos", self()),
		externMethod("neg", self()),
		externMethod("add", self(), self()),
		externMethod("sub", self(), self()),
		externMethod("mul", self(), self()),
		externMethod("div", self(), self()),
		externMethod("rem", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// booleanMethods is the operator-method signature set of the boolean primitive:
// logical ops return self, equality returns bool, and not returns self.
func booleanMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("not", self()),
		externMethod("anan", self(), self()),
		externMethod("oror", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
	}
}

// stringMethods is the operator-method signature set of the string primitive:
// add concatenates (returning self), and equality and the lexicographic
// comparisons return bool. It mirrors the prelude's string.belt.
func stringMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("add", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// datetimeType and durationType are the cross-referenced builtin types in the
// datetime/duration operator signatures: the two interoperate (dt ± dr,
// dt - dt, dr + dt), so each names the other in its overloads.
var (
	datetimeType ir.Type = &ir.Builtin{Name: NameDatetime}
	durationType ir.Type = &ir.Builtin{Name: "duration"}
	intType      ir.Type = &ir.Builtin{Name: "nint"}
)

// comparisonMethods is the equality and ordering signature set shared by the
// datetime and duration primitives: both compare against self and return bool.
func comparisonMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// datetimeMethods is the operator-method signature set of the datetime
// primitive. sub is overloaded by argument type: another instant yields the
// span between them, a duration yields the earlier instant. It mirrors the
// prelude's datetime.belt.
func datetimeMethods() []*ir.Method {
	return append(comparisonMethods(),
		externMethod("add", self(), durationType),
		externMethod("sub", durationType, self()),
		externMethod("sub", self(), durationType),
	)
}

// durationMethods is the operator-method signature set of the duration
// primitive. add is overloaded by argument type: another span sums, a datetime
// yields the instant the span after it. It mirrors the prelude's duration.belt.
func durationMethods() []*ir.Method {
	return append(comparisonMethods(),
		externMethod("add", self(), self()),
		externMethod("add", datetimeType, datetimeType),
		externMethod("sub", self(), self()),
		externMethod("mul", self(), intType),
	)
}

// EnumComparisonMethods is the operator-method signature set every enum
// carries: equality and the ordering comparisons, each against self and
// returning bool. An enum is a nominal value set — it does not inherit its
// base type's arithmetic — so these six are the only operators it has beyond
// its own impl. The result and self operands let the bidirectional checker
// require both sides be the same enum (Match against self unifies the
// receiver), so a comparison across two enums or against the base type is a
// type error. The evaluator implements them directly on the enum value rather
// than through the registry, since enums are user-defined.
func EnumComparisonMethods() []*ir.Method {
	return comparisonMethods()
}

// errorMethods is the method signature set of the error primitive: message
// reads back the message the error was constructed with. It mirrors the
// prelude's error.belt.
func errorMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("message", stringType),
	}
}
