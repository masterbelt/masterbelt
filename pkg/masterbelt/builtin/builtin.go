// Package builtin is the registry of native primitive types: the single
// injection point that supplies the value ranges and operator implementations
// the type system and the evaluator need, keyed on a builtin's name.
//
// A primitive's structure — its name and its operator method signatures — is
// described here as an *ir.TypeDef, so the type system resolves a primitive's
// methods through exactly the same path as a user type's. Its native semantics —
// value range and the evaluation of each operator — is described by a NativeType
// and a set of Intrinsics. Adding a primitive (int128, a float, a string) is
// therefore a matter of registering a descriptor here and declaring it in the
// prelude; nothing in the type system hardcodes the set of primitives or their
// ranges.
//
// The prelude (pkg/masterbelt/builtin/belt) declares the same primitives in
// masterbelt syntax. Package semantic loads it and validates it against this
// registry, so the two cannot drift.
package builtin

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
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
func (k IntKind) bounds() (min, max *big.Int) {
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
// integer), or a flag marking the boolean or null type.
type NativeType struct {
	Name string
	Int  *IntKind // non-nil for an integer primitive
	Bool bool     // the boolean type
	Null bool     // the null type
}

// IsInteger reports whether the primitive is an integer type.
func (n *NativeType) IsInteger() bool { return n.Int != nil }

// IsBoolean reports whether the primitive is the boolean type.
func (n *NativeType) IsBoolean() bool { return n.Bool }

// Fits reports whether v is within the primitive's value range. A non-integer
// primitive (or an arbitrary-precision integer) accepts any value within its
// (possibly half-open) range.
func (n *NativeType) Fits(v *big.Int) bool {
	if n.Int == nil {
		return true
	}
	min, max := n.Int.bounds()
	if min != nil && v.Cmp(min) < 0 {
		return false
	}
	if max != nil && v.Cmp(max) > 0 {
		return false
	}
	return true
}

// Intrinsic is the native implementation of an extern method: it computes the
// method's value from the receiver and argument constants (all guaranteed
// non-nil by the caller), or returns nil when the operation has no value — a
// type-incorrect program, or a division by zero.
type Intrinsic func(recv *ir.Constant, args []*ir.Constant) *ir.Constant

// Registry is a set of native primitives. Build the standard one with Default.
type Registry struct {
	order      []string
	defs       map[string]*ir.TypeDef
	natives    map[string]*NativeType
	intrinsics map[string]map[string]Intrinsic
}

// Lookup returns the type definition of the primitive named name.
func (r *Registry) Lookup(name string) (*ir.TypeDef, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Native returns the native descriptor of the primitive named name.
func (r *Registry) Native(name string) (*NativeType, bool) {
	n, ok := r.natives[name]
	return n, ok
}

// Intrinsic returns the native implementation of the named method on the named
// primitive type.
func (r *Registry) Intrinsic(typeName, method string) (Intrinsic, bool) {
	ms, ok := r.intrinsics[typeName]
	if !ok {
		return nil, false
	}
	fn, ok := ms[method]
	return fn, ok
}

// Names returns the primitive names in registration order.
func (r *Registry) Names() []string { return r.order }

// boolType is the shared boolean primitive type used in operator-method
// signatures (the result of the comparison and equality methods).
var boolType ir.Type = &ir.Builtin{Name: "bool"}

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

// --- intrinsics -------------------------------------------------------------

// unaryInt is a nullary-argument integer intrinsic (pos, neg).
func unaryInt(f func(a *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 0 || r.Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int)
	}
}

// binaryInt is a one-argument integer intrinsic over two integer operands.
func binaryInt(f func(a, b *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstInt || args[0].Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int, args[0].Int)
	}
}

// binaryBool is a one-argument boolean intrinsic over two boolean operands.
func binaryBool(f func(a, b bool) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstBool || args[0].Kind != ir.ConstBool {
			return nil
		}
		return f(r.Bool, args[0].Bool)
	}
}

func integerIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"pos": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Set(a)) }),
		"neg": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Neg(a)) }),
		"add": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Add(a, b)) }),
		"sub": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Sub(a, b)) }),
		"mul": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Mul(a, b)) }),
		"div": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Quo(a, b))
		}),
		"rem": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Rem(a, b))
		}),
		"eql":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) == 0) }),
		"neq":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) != 0) }),
		"lt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) < 0) }),
		"lteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) <= 0) }),
		"gt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) > 0) }),
		"gteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) >= 0) }),
	}
}

func booleanIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"not":  func(r *ir.Constant, args []*ir.Constant) *ir.Constant { return notBool(r, args) },
		"anan": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a && b) }),
		"oror": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a || b) }),
		"eql":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a != b) }),
	}
}

func notBool(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstBool {
		return nil
	}
	return ir.BoolConstant(!r.Bool)
}

// --- the standard registry --------------------------------------------------

var integerSpecs = []struct {
	name string
	kind IntKind
}{
	{"int", IntKind{Signed: true, Bits: 0}},
	{"int8", IntKind{Signed: true, Bits: 8}},
	{"int16", IntKind{Signed: true, Bits: 16}},
	{"int32", IntKind{Signed: true, Bits: 32}},
	{"int64", IntKind{Signed: true, Bits: 64}},
	{"uint", IntKind{Signed: false, Bits: 0}},
	{"uint8", IntKind{Signed: false, Bits: 8}},
	{"uint16", IntKind{Signed: false, Bits: 16}},
	{"uint32", IntKind{Signed: false, Bits: 32}},
	{"uint64", IntKind{Signed: false, Bits: 64}},
}

// Default returns the standard registry: the integer family, bool, and null.
func Default() *Registry {
	r := &Registry{
		defs:       map[string]*ir.TypeDef{},
		natives:    map[string]*NativeType{},
		intrinsics: map[string]map[string]Intrinsic{},
	}
	for _, spec := range integerSpecs {
		kind := spec.kind
		r.register(spec.name, &NativeType{Name: spec.name, Int: &kind}, integerMethods(), integerIntrinsics())
	}
	r.register("bool", &NativeType{Name: "bool", Bool: true}, booleanMethods(), booleanIntrinsics())
	r.register("null", &NativeType{Name: "null", Null: true}, nil, nil)
	return r
}

func (r *Registry) register(name string, native *NativeType, methods []*ir.Method, intrinsics map[string]Intrinsic) {
	r.order = append(r.order, name)
	r.defs[name] = &ir.TypeDef{
		Name:    name,
		Public:  true,
		Body:    &ir.Builtin{Name: name},
		Methods: methods,
		Builtin: true,
	}
	r.natives[name] = native
	if intrinsics != nil {
		r.intrinsics[name] = intrinsics
	}
}
